/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package zone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// defaultZoneConfigInterval is the fallback requeue interval when Spec.Interval is unset.
const defaultZoneConfigInterval = 30 * time.Minute

// CloudflareZoneConfigReconciler drives the lifecycle of a CloudflareZoneConfig
// CR: credentials → resolve zone → apply six typed groups (SSL, Security,
// Performance, Network, DNS, BotManagement) → reflect status. Fast-skips
// per-setting API calls when the settings-relevant spec hash is unchanged.
//
// Zone settings persist server-side in Cloudflare; deletion of the CR does
// NOT revert them. Reconcile only drops the finalizer on the delete path.
type CloudflareZoneConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder is wired by the manager setup (T18). Nil is tolerated; the
	// per-group transition-event emitter no-ops without a recorder.
	Recorder record.EventRecorder
	// ZoneConfigClientFn returns a Cloudflare ZoneConfigClient for the
	// resolved credentials. Tests inject an in-memory mock.
	ZoneConfigClientFn func(cloudflare.Credentials) (cloudflare.ZoneConfigClient, error)
}

// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezoneconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezoneconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezoneconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one iteration of the CloudflareZoneConfig state machine.
func (r *CloudflareZoneConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cloudflarezoneconfig", req.NamespacedName)

	var cfg v1alpha1.CloudflareZoneConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path: zone settings are not "owned" objects in Cloudflare
	// (they persist server-side and reverting them is out of scope), so we
	// only drop the finalizer.
	if !cfg.DeletionTimestamp.IsZero() {
		if reconcile.RemoveFinalizer(&cfg, conventions.FinalizerName) {
			if err := r.Update(ctx, &cfg); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if reconcile.EnsureFinalizer(&cfg, conventions.FinalizerName) {
		if err := r.Update(ctx, &cfg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, cfg.Spec.Cloudflare, cfg.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if halt != nil {
		cfg.Status.Conditions = reconcile.SetReady(cfg.Status.Conditions, metav1.ConditionFalse,
			conventions.ReasonCredentialsUnavailable, "cloudflare credentials unavailable")
		cfg.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonCredentialsUnavailable)
		if uerr := r.Status().Update(ctx, &cfg); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return *halt, nil
	}

	// Resolve zone identity (zoneID or zoneRef). We always resolve fresh so
	// Status.ZoneID reflects the current resolution even on the fast-skip
	// pass (e.g. if the user retargeted ZoneRef between reconciles).
	zres, err := reconcile.ResolveZoneID(ctx, r.Client, reconcile.ZoneRefInputs{
		ZoneID: cfg.Spec.ZoneID, ZoneRef: cfg.Spec.ZoneRef,
	}, cfg.Namespace)
	if err != nil {
		if stderrors.Is(err, reconcile.ErrZoneRefNotFound) {
			return r.haltDependency(ctx, &cfg, err.Error())
		}
		return ctrl.Result{}, err
	}
	if zres.ZoneID == "" {
		// ZoneRef target exists but its status has no zone ID yet — wait.
		return r.haltDependency(ctx, &cfg, "zoneRef target has no status.zoneID yet")
	}
	zoneID := zres.ZoneID
	cfg.Status.ZoneID = zoneID

	interval := defaultZoneConfigInterval
	if cfg.Spec.Interval != nil && cfg.Spec.Interval.Duration > 0 {
		interval = cfg.Spec.Interval.Duration
	}

	// Fast-skip: if the settings-relevant spec hash matches the last
	// applied hash AND we're already Ready, skip all per-setting API calls.
	// Out-of-band dashboard edits won't be reverted until the K8s spec
	// itself changes. Exit BEFORE constructing the Cloudflare client
	// (pre-flight contract: no client construction on the fast-skip path).
	desiredHash := hashZoneConfigSpec(&cfg.Spec)
	if cfg.Status.AppliedSpecHash == desiredHash && cfg.Status.Phase == v1alpha1.PhaseReady {
		logger.V(1).Info("zoneconfig spec unchanged, fast-skip", "hash", desiredHash)
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	zcc, err := r.ZoneConfigClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Snapshot prior per-group condition statuses so we can emit transition events.
	prior := snapshotGroupConditions(cfg.Status.Conditions)

	// Apply each of the 6 groups independently. A failure in one group must
	// not short-circuit the others.
	results := []groupResult{
		applySSLGroup(ctx, zcc, zoneID, cfg.Spec.SSL),
		applySecurityGroup(ctx, zcc, zoneID, cfg.Spec.Security),
		applyPerformanceGroup(ctx, zcc, zoneID, cfg.Spec.Performance),
		applyNetworkGroup(ctx, zcc, zoneID, cfg.Spec.Network),
		applyDNSGroup(ctx, zcc, zoneID, cfg.Spec.DNS),
		applyBotManagementGroup(ctx, zcc, zoneID, cfg.Spec.BotManagement),
	}

	// Persist per-group conditions for configured groups only (unconfigured
	// groups leave the slot untouched) and pick the first failure for Ready.
	allOK := true
	var firstFailReason, firstFailMsg string
	for _, g := range results {
		if g.skip {
			continue
		}
		cfg.Status.Conditions = reconcile.SetCondition(cfg.Status.Conditions,
			g.conditionType, g.status(), g.reason(), g.message())
		if g.status() != metav1.ConditionTrue {
			allOK = false
			if firstFailReason == "" {
				firstFailReason, firstFailMsg = g.reason(), g.message()
			}
		}
	}
	if allOK {
		cfg.Status.Conditions = reconcile.SetReady(cfg.Status.Conditions, metav1.ConditionTrue,
			conventions.ReasonReady, "all configured groups applied")
		cfg.Status.Phase = reconcile.DerivePhase(metav1.ConditionTrue, conventions.ReasonReady)
		cfg.Status.AppliedSpecHash = desiredHash
	} else {
		cfg.Status.Conditions = reconcile.SetReady(cfg.Status.Conditions, metav1.ConditionFalse,
			firstFailReason, firstFailMsg)
		cfg.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, firstFailReason)
		// Do NOT advance AppliedSpecHash; partial apply must retry next reconcile.
	}

	// Emit transition events after conditions are finalized so we compare
	// the new status against the snapshot taken before this pass.
	r.emitGroupTransitionEvents(&cfg, prior, results)

	now := metav1.Now()
	cfg.Status.LastSyncedAt = &now
	cfg.Status.ObservedGeneration = cfg.Generation

	if err := r.Status().Update(ctx, &cfg); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

// haltDependency persists a DependencyMissing Ready=False with the given
// message and requeues after a short interval. Used when zone resolution
// can't proceed because the referenced CloudflareZone isn't ready yet.
func (r *CloudflareZoneConfigReconciler) haltDependency(ctx context.Context, cfg *v1alpha1.CloudflareZoneConfig, msg string) (ctrl.Result, error) {
	return reconcile.HaltDependency(ctx, r.Client, cfg, &cfg.Status.Conditions, &cfg.Status.Phase, msg, 30*time.Second)
}

// groupResult captures the outcome of applying a single settings group. It
// is returned by each apply* helper and consumed by the outer Reconcile
// loop; apply helpers never write status directly.
type groupResult struct {
	conditionType string // e.g. ConditionTypeSSLApplied
	groupLabel    string // human-readable label for messages/events
	appliedReason string // e.g. ReasonSSLApplied — set on success
	skip          bool   // true when the spec field is nil (group not configured)
	err           error  // nil on success
	count         int    // number of settings touched
}

func (g groupResult) status() metav1.ConditionStatus {
	if g.skip {
		return metav1.ConditionUnknown
	}
	if g.err != nil {
		return metav1.ConditionFalse
	}
	return metav1.ConditionTrue
}

func (g groupResult) reason() string {
	if g.err == nil {
		return g.appliedReason
	}
	if stderrors.Is(g.err, cloudflare.ErrPlanTierInsufficient) {
		return conventions.ReasonPlanTierInsufficient
	}
	return conventions.ReasonDegraded
}

func (g groupResult) message() string {
	if g.err == nil {
		return fmt.Sprintf("applied %d %s setting(s)", g.count, g.groupLabel)
	}
	return g.err.Error()
}

// settingUpdate is a single (id, value) pair fed to UpdateSetting. The
// appendX helpers convert typed v1alpha1 structs to a flat list, using the
// exact Cloudflare zone-settings setting IDs.
type settingUpdate struct {
	id    string
	value any
}

func appendIfSet[T any](u []settingUpdate, id string, v *T) []settingUpdate {
	if v == nil {
		return u
	}
	return append(u, settingUpdate{id, *v})
}

func appendSSL(u []settingUpdate, s *v1alpha1.SSLSettings) []settingUpdate {
	if s == nil {
		return u
	}
	u = appendIfSet(u, "ssl", s.Mode)
	u = appendIfSet(u, "min_tls_version", s.MinTLSVersion)
	u = appendIfSet(u, "tls_1_3", s.TLS13)
	u = appendIfSet(u, "always_use_https", s.AlwaysUseHTTPS)
	u = appendIfSet(u, "automatic_https_rewrites", s.AutomaticHTTPSRewrites)
	u = appendIfSet(u, "opportunistic_encryption", s.OpportunisticEncryption)
	return u
}

// putIfSet stores *v under key k in m if v is non-nil. Used to build the
// nested map payloads for security_header and minify without repeated nil
// checks at the call site.
func putIfSet[T any](m map[string]any, k string, v *T) {
	if v != nil {
		m[k] = *v
	}
}

func appendSecurity(u []settingUpdate, s *v1alpha1.SecuritySettings) []settingUpdate {
	if s == nil {
		return u
	}
	u = appendIfSet(u, "security_level", s.SecurityLevel)
	u = appendIfSet(u, "challenge_ttl", s.ChallengeTTL)
	u = appendIfSet(u, "browser_check", s.BrowserCheck)
	u = appendIfSet(u, "email_obfuscation", s.EmailObfuscation)
	u = appendIfSet(u, "server_side_exclude", s.ServerSideExclude)
	u = appendIfSet(u, "hotlink_protection", s.HotlinkProtection)
	if sh := s.SecurityHeader; sh != nil {
		sts := map[string]any{}
		putIfSet(sts, "enabled", sh.Enabled)
		putIfSet(sts, "max_age", sh.MaxAge)
		putIfSet(sts, "include_subdomains", sh.IncludeSubdomains)
		putIfSet(sts, "preload", sh.Preload)
		putIfSet(sts, "nosniff", sh.Nosniff)
		if len(sts) > 0 {
			u = append(u, settingUpdate{id: "security_header",
				value: map[string]any{"strict_transport_security": sts}})
		}
	}
	return u
}

func appendPerformance(u []settingUpdate, p *v1alpha1.PerformanceSettings) []settingUpdate {
	if p == nil {
		return u
	}
	u = appendIfSet(u, "cache_level", p.CacheLevel)
	u = appendIfSet(u, "browser_cache_ttl", p.BrowserCacheTTL)
	if mi := p.Minify; mi != nil {
		mv := map[string]any{}
		putIfSet(mv, "css", mi.CSS)
		putIfSet(mv, "html", mi.HTML)
		putIfSet(mv, "js", mi.JS)
		if len(mv) > 0 {
			u = append(u, settingUpdate{"minify", mv})
		}
	}
	u = appendIfSet(u, "polish", p.Polish)
	u = appendIfSet(u, "brotli", p.Brotli)
	u = appendIfSet(u, "early_hints", p.EarlyHints)
	u = appendIfSet(u, "http2", p.HTTP2)
	u = appendIfSet(u, "http3", p.HTTP3)
	u = appendIfSet(u, "always_online", p.AlwaysOnline)
	u = appendIfSet(u, "rocket_loader", p.RocketLoader)
	return u
}

func appendNetwork(u []settingUpdate, n *v1alpha1.NetworkSettings) []settingUpdate {
	if n == nil {
		return u
	}
	u = appendIfSet(u, "ipv6", n.IPv6)
	u = appendIfSet(u, "websockets", n.WebSockets)
	u = appendIfSet(u, "pseudo_ipv4", n.PseudoIPv4)
	u = appendIfSet(u, "ip_geolocation", n.IPGeolocation)
	u = appendIfSet(u, "opportunistic_onion", n.OpportunisticOnion)
	return u
}

func appendDNS(u []settingUpdate, d *v1alpha1.DNSSettings) []settingUpdate {
	if d == nil {
		return u
	}
	u = appendIfSet(u, "cname_flattening", d.CNAMEFlattening)
	return u
}

// applySettingsGroup is the shared body of applySSLGroup / applySecurityGroup /
// applyPerformanceGroup / applyNetworkGroup / applyDNSGroup. It returns skip
// if settings is nil; otherwise iterates through updates and stops on the
// first UpdateSetting error (so the caller's reason() can classify it).
func applySettingsGroup[T any](
	ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string,
	condType, label, appliedReason string,
	settings *T,
	build func([]settingUpdate, *T) []settingUpdate,
) groupResult {
	g := groupResult{conditionType: condType, groupLabel: label, appliedReason: appliedReason}
	if settings == nil {
		g.skip = true
		return g
	}
	updates := build(nil, settings)
	for _, s := range updates {
		if err := c.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			g.err = fmt.Errorf("update setting %s: %w", s.id, err)
			return g
		}
	}
	g.count = len(updates)
	return g
}

func applySSLGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.SSLSettings) groupResult {
	return applySettingsGroup(ctx, c, zoneID, conventions.ConditionTypeSSLApplied, "SSL", conventions.ReasonSSLApplied, s, appendSSL)
}

func applySecurityGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.SecuritySettings) groupResult {
	return applySettingsGroup(ctx, c, zoneID, conventions.ConditionTypeSecurityApplied, "Security", conventions.ReasonSecurityApplied, s, appendSecurity)
}

func applyPerformanceGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.PerformanceSettings) groupResult {
	return applySettingsGroup(ctx, c, zoneID, conventions.ConditionTypePerformanceApplied, "Performance", conventions.ReasonPerformanceApplied, s, appendPerformance)
}

func applyNetworkGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.NetworkSettings) groupResult {
	return applySettingsGroup(ctx, c, zoneID, conventions.ConditionTypeNetworkApplied, "Network", conventions.ReasonNetworkApplied, s, appendNetwork)
}

func applyDNSGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.DNSSettings) groupResult {
	return applySettingsGroup(ctx, c, zoneID, conventions.ConditionTypeDNSApplied, "DNS", conventions.ReasonDNSApplied, s, appendDNS)
}

// applyBotManagementGroup uses the dedicated /bot_management endpoint rather
// than UpdateSetting. A plan-tier 403 surfaces as ErrPlanTierInsufficient
// from the underlying client and is classified by groupResult.reason().
func applyBotManagementGroup(ctx context.Context, c cloudflare.ZoneConfigClient, zoneID string, s *v1alpha1.BotManagementSettings) groupResult {
	g := groupResult{
		conditionType: conventions.ConditionTypeBotManagementApplied,
		groupLabel:    "BotManagement",
		appliedReason: conventions.ReasonBotManagementApplied,
	}
	if s == nil {
		g.skip = true
		return g
	}
	if err := c.UpdateBotManagement(ctx, zoneID, cloudflare.BotManagementConfig{EnableJS: s.EnableJS, FightMode: s.FightMode}); err != nil {
		g.err = fmt.Errorf("update bot management: %w", err)
		return g
	}
	g.count = 1
	return g
}

// snapshotGroupConditions captures the pre-reconcile status of each per-group
// condition so we can detect transitions and emit events accordingly.
func snapshotGroupConditions(conds []metav1.Condition) map[string]metav1.ConditionStatus {
	out := map[string]metav1.ConditionStatus{}
	for _, c := range conds {
		out[c.Type] = c.Status
	}
	return out
}

// emitGroupTransitionEvents emits a Normal or Warning event for each group
// whose status changed since the prior reconcile. Unconfigured groups
// (skip=true) emit no events. A nil recorder makes this a no-op so tests
// without an event sink remain safe.
func (r *CloudflareZoneConfigReconciler) emitGroupTransitionEvents(
	obj client.Object,
	prior map[string]metav1.ConditionStatus,
	results []groupResult,
) {
	if r.Recorder == nil {
		return
	}
	for _, g := range results {
		if g.skip {
			continue
		}
		newStatus := g.status()
		oldStatus, had := prior[g.conditionType]
		if had && oldStatus == newStatus {
			continue
		}
		switch newStatus {
		case metav1.ConditionTrue:
			r.Recorder.Eventf(obj, corev1.EventTypeNormal, "SettingsApplied",
				"%s applied (%d setting(s))", g.groupLabel, g.count)
		default:
			r.Recorder.Eventf(obj, corev1.EventTypeWarning, "SettingsApplyFailed",
				"%s: %s: %s", g.groupLabel, g.reason(), g.message())
		}
	}
}

// hashZoneConfigSpec returns a sha256 hex digest over the settings-relevant
// spec fields. Operational fields (Interval, Cloudflare, ZoneID/Ref) are
// excluded so changing them doesn't spuriously re-apply settings.
func hashZoneConfigSpec(spec *v1alpha1.CloudflareZoneConfigSpec) string {
	payload := struct {
		SSL           *v1alpha1.SSLSettings           `json:"ssl,omitempty"`
		Security      *v1alpha1.SecuritySettings      `json:"security,omitempty"`
		Performance   *v1alpha1.PerformanceSettings   `json:"performance,omitempty"`
		Network       *v1alpha1.NetworkSettings       `json:"network,omitempty"`
		DNS           *v1alpha1.DNSSettings           `json:"dns,omitempty"`
		BotManagement *v1alpha1.BotManagementSettings `json:"botManagement,omitempty"`
	}{spec.SSL, spec.Security, spec.Performance, spec.Network, spec.DNS, spec.BotManagement}

	data, err := json.Marshal(payload)
	if err != nil {
		// Marshalling these plain value structs cannot fail at runtime.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
