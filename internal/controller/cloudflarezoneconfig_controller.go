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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// CloudflareZoneConfigReconciler reconciles a CloudflareZoneConfig object
type CloudflareZoneConfigReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Recorder      record.EventRecorder
	ClientFactory *cfclient.ClientFactory
	ZoneClientFn  func(apiToken string) cfclient.ZoneClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezoneconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezoneconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezoneconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareZoneConfig resource. It handles applying zone settings to
// Cloudflare and tracking the applied settings count.
func (r *CloudflareZoneConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the CR
	var zoneConfig cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(ctx, req.NamespacedName, &zoneConfig); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	preStatus := zoneConfig.Status.DeepCopy()

	// 2. Handle deletion
	if !zoneConfig.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&zoneConfig, cloudflarev1alpha1.FinalizerName) {
			return r.reconcileDelete(ctx, &zoneConfig)
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&zoneConfig, cloudflarev1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&zoneConfig, cloudflarev1alpha1.FinalizerName)
		if err := r.Update(ctx, &zoneConfig); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 3.5. Resolve zone ID
	resolvedZoneID, err := ResolveZoneID(ctx, r.Client, &zoneConfig)
	if err != nil {
		if stderrors.Is(err, ErrZoneRefNotReady) {
			logger.Info("waiting for zone reference", "error", err.Error())
		} else {
			logger.Error(err, "failed to resolve zone ID")
		}
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err, 30*time.Second)
	}
	zoneConfig.Status.ZoneID = resolvedZoneID

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, zoneConfig.Spec.SecretRef.Name, zoneConfig.Namespace)
	if err != nil {
		logger.Error(err, "failed to get API token")
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the zone config.
	// Per-group conditions set inside reconcileZoneConfig (via status.SetCondition)
	// survive failReconcile because failReconcile only mutates the Ready slot via
	// status.SetReady. Both go through the same Status().Update call.
	result, err := r.reconcileZoneConfig(ctx, &zoneConfig, r.zoneClient(apiToken), resolvedZoneID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonPartialApply, err, time.Minute)
	}

	// 7. Persist status only if anything materially changed.
	zoneConfig.Status.ObservedGeneration = zoneConfig.Generation
	status.SetReady(&zoneConfig.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Zone config synced", zoneConfig.Generation)
	if !reflect.DeepEqual(preStatus, &zoneConfig.Status) {
		now := metav1.Now()
		zoneConfig.Status.LastSyncedAt = &now
		if err := r.Status().Update(ctx, &zoneConfig); err != nil {
			return ctrl.Result{}, err
		}
	}

	return result, nil
}

// settingUpdate represents a single zone setting to apply.
type settingUpdate struct {
	id    string
	value any
}

// appendIfSet appends a settingUpdate if value is non-nil, dereferencing it.
func appendIfSet[T any](updates []settingUpdate, id string, value *T) []settingUpdate {
	if value == nil {
		return updates
	}
	return append(updates, settingUpdate{id, *value})
}

func appendSSL(updates []settingUpdate, ssl *cloudflarev1alpha1.SSLSettings) []settingUpdate {
	if ssl == nil {
		return updates
	}
	updates = appendIfSet(updates, "ssl", ssl.Mode)
	updates = appendIfSet(updates, "min_tls_version", ssl.MinTLSVersion)
	updates = appendIfSet(updates, "tls_1_3", ssl.TLS13)
	updates = appendIfSet(updates, "always_use_https", ssl.AlwaysUseHTTPS)
	updates = appendIfSet(updates, "automatic_https_rewrites", ssl.AutomaticHTTPSRewrites)
	updates = appendIfSet(updates, "opportunistic_encryption", ssl.OpportunisticEncryption)
	return updates
}

func appendSecurity(updates []settingUpdate, sec *cloudflarev1alpha1.SecuritySettings) []settingUpdate {
	if sec == nil {
		return updates
	}
	updates = appendIfSet(updates, "security_level", sec.SecurityLevel)
	updates = appendIfSet(updates, "challenge_ttl", sec.ChallengeTTL)
	updates = appendIfSet(updates, "browser_check", sec.BrowserCheck)
	updates = appendIfSet(updates, "email_obfuscation", sec.EmailObfuscation)
	return updates
}

func appendPerformance(updates []settingUpdate, perf *cloudflarev1alpha1.PerformanceSettings) []settingUpdate {
	if perf == nil {
		return updates
	}
	updates = appendIfSet(updates, "cache_level", perf.CacheLevel)
	updates = appendIfSet(updates, "browser_cache_ttl", perf.BrowserCacheTTL)
	if perf.Minify != nil {
		minifyValue := map[string]string{}
		if perf.Minify.CSS != nil {
			minifyValue["css"] = *perf.Minify.CSS
		}
		if perf.Minify.HTML != nil {
			minifyValue["html"] = *perf.Minify.HTML
		}
		if perf.Minify.JS != nil {
			minifyValue["js"] = *perf.Minify.JS
		}
		if len(minifyValue) > 0 {
			updates = append(updates, settingUpdate{"minify", minifyValue})
		}
	}
	updates = appendIfSet(updates, "polish", perf.Polish)
	updates = appendIfSet(updates, "brotli", perf.Brotli)
	updates = appendIfSet(updates, "early_hints", perf.EarlyHints)
	updates = appendIfSet(updates, "http2", perf.HTTP2)
	updates = appendIfSet(updates, "http3", perf.HTTP3)
	return updates
}

func appendNetwork(updates []settingUpdate, net *cloudflarev1alpha1.NetworkSettings) []settingUpdate {
	if net == nil {
		return updates
	}
	updates = appendIfSet(updates, "ipv6", net.IPv6)
	updates = appendIfSet(updates, "websockets", net.WebSockets)
	updates = appendIfSet(updates, "pseudo_ipv4", net.PseudoIPv4)
	updates = appendIfSet(updates, "ip_geolocation", net.IPGeolocation)
	updates = appendIfSet(updates, "opportunistic_onion", net.OpportunisticOnion)
	return updates
}

// groupResult captures the outcome of applying a single settings group.
type groupResult struct {
	conditionType string // e.g., ConditionTypeSSLApplied
	groupLabel    string // human-readable, e.g., "SSL"
	configured    bool   // true if the user set this section
	err           error  // nil on success or NotConfigured
	settingsCount int    // count of settings touched on success
}

// status returns the metav1.ConditionStatus for this group.
func (g groupResult) status() metav1.ConditionStatus {
	if !g.configured {
		return metav1.ConditionFalse
	}
	if g.err != nil {
		return metav1.ConditionFalse
	}
	return metav1.ConditionTrue
}

// reason returns the condition reason for this group.
func (g groupResult) reason() string {
	if !g.configured {
		return cloudflarev1alpha1.ReasonNotConfigured
	}
	if g.err == nil {
		return cloudflarev1alpha1.ReasonApplied
	}
	if cfclient.IsPermissionDenied(g.err) {
		return cloudflarev1alpha1.ReasonPermissionDenied
	}
	return cloudflarev1alpha1.ReasonCloudflareError
}

// message returns the condition message for this group.
func (g groupResult) message() string {
	if !g.configured {
		return fmt.Sprintf("%s settings not configured", g.groupLabel)
	}
	if g.err == nil {
		return fmt.Sprintf("applied %d %s settings", g.settingsCount, g.groupLabel)
	}
	return g.err.Error()
}

func (r *CloudflareZoneConfigReconciler) reconcileZoneConfig(ctx context.Context, zoneConfig *cloudflarev1alpha1.CloudflareZoneConfig, zoneClient cfclient.ZoneClient, zoneID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	requeueAfter := 30 * time.Minute
	if zoneConfig.Spec.Interval != nil {
		requeueAfter = zoneConfig.Spec.Interval.Duration
	}

	// Skip the whole apply loop if the settings-relevant spec hasn't changed
	// since the last successful reconcile. Out-of-band dashboard edits won't
	// be reverted until the K8s spec itself changes.
	desiredHash := hashZoneConfigSpec(&zoneConfig.Spec)
	if zoneConfig.Status.AppliedSpecHash == desiredHash {
		logger.V(1).Info("zone config spec unchanged, skipping settings apply", "hash", desiredHash)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Snapshot prior per-group condition status so we can emit transition events.
	priorStatuses := snapshotGroupStatuses(zoneConfig.Status.Conditions)

	results := []groupResult{
		applySSLGroup(ctx, zoneClient, zoneID, zoneConfig.Spec.SSL),
		applySecurityGroup(ctx, zoneClient, zoneID, zoneConfig.Spec.Security),
		applyPerformanceGroup(ctx, zoneClient, zoneID, zoneConfig.Spec.Performance),
		applyNetworkGroup(ctx, zoneClient, zoneID, zoneConfig.Spec.Network),
		applyBotManagementGroup(ctx, zoneClient, zoneID, zoneConfig.Spec.BotManagement),
	}

	// Persist per-group conditions.
	for _, g := range results {
		status.SetCondition(&zoneConfig.Status.Conditions, g.conditionType, g.status(), g.reason(), g.message(), zoneConfig.Generation)
	}

	// Emit transition events.
	r.emitGroupTransitionEvents(zoneConfig, priorStatuses, results)

	// Aggregate failures.
	var failed []groupResult
	for _, g := range results {
		if g.configured && g.err != nil {
			failed = append(failed, g)
		}
	}
	if len(failed) > 0 {
		// Don't update appliedSpecHash — failed groups must retry next reconcile.
		return ctrl.Result{}, aggregateErr(failed)
	}

	// All configured groups succeeded.
	zoneConfig.Status.AppliedSpecHash = desiredHash

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// applySSLGroup applies the SSL settings group, if configured.
func applySSLGroup(ctx context.Context, zoneClient cfclient.ZoneClient, zoneID string, ssl *cloudflarev1alpha1.SSLSettings) groupResult {
	g := groupResult{conditionType: cloudflarev1alpha1.ConditionTypeSSLApplied, groupLabel: "SSL"}
	if ssl == nil {
		return g
	}
	g.configured = true
	updates := appendSSL(nil, ssl)
	for _, s := range updates {
		if err := zoneClient.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			g.err = fmt.Errorf("update setting %s: %w", s.id, err)
			return g
		}
	}
	g.settingsCount = len(updates)
	return g
}

// applySecurityGroup applies the Security settings group, if configured.
func applySecurityGroup(ctx context.Context, zoneClient cfclient.ZoneClient, zoneID string, sec *cloudflarev1alpha1.SecuritySettings) groupResult {
	g := groupResult{conditionType: cloudflarev1alpha1.ConditionTypeSecurityApplied, groupLabel: "Security"}
	if sec == nil {
		return g
	}
	g.configured = true
	updates := appendSecurity(nil, sec)
	for _, s := range updates {
		if err := zoneClient.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			g.err = fmt.Errorf("update setting %s: %w", s.id, err)
			return g
		}
	}
	g.settingsCount = len(updates)
	return g
}

// applyPerformanceGroup applies the Performance settings group, if configured.
func applyPerformanceGroup(ctx context.Context, zoneClient cfclient.ZoneClient, zoneID string, perf *cloudflarev1alpha1.PerformanceSettings) groupResult {
	g := groupResult{conditionType: cloudflarev1alpha1.ConditionTypePerformanceApplied, groupLabel: "Performance"}
	if perf == nil {
		return g
	}
	g.configured = true
	updates := appendPerformance(nil, perf)
	for _, s := range updates {
		if err := zoneClient.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			g.err = fmt.Errorf("update setting %s: %w", s.id, err)
			return g
		}
	}
	g.settingsCount = len(updates)
	return g
}

// applyNetworkGroup applies the Network settings group, if configured.
func applyNetworkGroup(ctx context.Context, zoneClient cfclient.ZoneClient, zoneID string, net *cloudflarev1alpha1.NetworkSettings) groupResult {
	g := groupResult{conditionType: cloudflarev1alpha1.ConditionTypeNetworkApplied, groupLabel: "Network"}
	if net == nil {
		return g
	}
	g.configured = true
	updates := appendNetwork(nil, net)
	for _, s := range updates {
		if err := zoneClient.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			g.err = fmt.Errorf("update setting %s: %w", s.id, err)
			return g
		}
	}
	g.settingsCount = len(updates)
	return g
}

// applyBotManagementGroup applies the BotManagement settings group, if configured.
func applyBotManagementGroup(ctx context.Context, zoneClient cfclient.ZoneClient, zoneID string, bm *cloudflarev1alpha1.BotManagementSettings) groupResult {
	g := groupResult{conditionType: cloudflarev1alpha1.ConditionTypeBotManagementApplied, groupLabel: "BotManagement"}
	if bm == nil {
		return g
	}
	g.configured = true
	config := cfclient.BotManagementConfig{
		EnableJS:  bm.EnableJS,
		FightMode: bm.FightMode,
	}
	if err := zoneClient.UpdateBotManagement(ctx, zoneID, config); err != nil {
		g.err = fmt.Errorf("update bot management: %w", err)
		return g
	}
	g.settingsCount = 1 // bot_management is one logical group/api call
	return g
}

// snapshotGroupStatuses returns a map of condition type -> status from the
// pre-reconcile condition list, used to detect transitions.
func snapshotGroupStatuses(conds []metav1.Condition) map[string]metav1.ConditionStatus {
	out := map[string]metav1.ConditionStatus{}
	for _, c := range conds {
		out[c.Type] = c.Status
	}
	return out
}

// emitGroupTransitionEvents emits SettingsApplied / SettingsApplyFailed events
// for groups whose status changed since the prior reconcile. Steady-state
// reconciles produce no events for these groups.
func (r *CloudflareZoneConfigReconciler) emitGroupTransitionEvents(
	obj client.Object,
	prior map[string]metav1.ConditionStatus,
	results []groupResult,
) {
	for _, g := range results {
		newStatus := g.status()
		oldStatus, hadPrior := prior[g.conditionType]
		// Skip NotConfigured groups — they don't change state in a way users care about.
		if !g.configured && (!hadPrior || oldStatus == newStatus) {
			continue
		}
		if hadPrior && oldStatus == newStatus {
			continue
		}
		if g.configured && g.err == nil {
			r.Recorder.Eventf(obj, corev1.EventTypeNormal, "SettingsApplied",
				"%s applied (%d settings)", g.groupLabel, g.settingsCount)
		} else if g.configured && g.err != nil {
			r.Recorder.Eventf(obj, corev1.EventTypeWarning, "SettingsApplyFailed",
				"%s failed: %s: %s", g.groupLabel, g.reason(), g.err.Error())
		}
	}
}

// aggregateErr produces a single error summarizing all failed groups,
// suitable for failReconcile to log/surface in the Ready condition.
// The summary uses each group's classified reason rather than the raw
// error string, so the wrapped underlying error (via %w) does not appear
// duplicated in the human-readable message.
func aggregateErr(failed []groupResult) error {
	parts := make([]string, 0, len(failed))
	for _, g := range failed {
		parts = append(parts, fmt.Sprintf("%s: %s", g.groupLabel, g.reason()))
	}
	// Wrap the first failed group's underlying error so errors.Is/As can still
	// classify it (e.g., IsPermissionDenied for a single 403).
	return fmt.Errorf("partial apply failed for %d group(s) [%s]: %w",
		len(failed), strings.Join(parts, ", "), failed[0].err)
}

// hashZoneConfigSpec returns a sha256 hex digest over the settings-relevant
// spec fields. Operational fields (Interval, SecretRef, ZoneID/Ref) are excluded
// so changing them doesn't spuriously re-apply settings.
func hashZoneConfigSpec(spec *cloudflarev1alpha1.CloudflareZoneConfigSpec) string {
	payload := struct {
		SSL           *cloudflarev1alpha1.SSLSettings           `json:"ssl,omitempty"`
		Security      *cloudflarev1alpha1.SecuritySettings      `json:"security,omitempty"`
		Performance   *cloudflarev1alpha1.PerformanceSettings   `json:"performance,omitempty"`
		Network       *cloudflarev1alpha1.NetworkSettings       `json:"network,omitempty"`
		BotManagement *cloudflarev1alpha1.BotManagementSettings `json:"botManagement,omitempty"`
	}{spec.SSL, spec.Security, spec.Performance, spec.Network, spec.BotManagement}

	// json.Marshal is deterministic for structs (field order is source order)
	// and omits nil pointers via omitempty, so semantically equal specs hash equal.
	data, err := json.Marshal(payload)
	if err != nil {
		// Marshalling these plain value structs cannot fail at runtime.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *CloudflareZoneConfigReconciler) reconcileDelete(ctx context.Context, zoneConfig *cloudflarev1alpha1.CloudflareZoneConfig) (ctrl.Result, error) {
	// Zone settings persist in Cloudflare — we don't revert on deletion
	controllerutil.RemoveFinalizer(zoneConfig, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, zoneConfig)
}

// zoneClient returns the test-injected ZoneClient if present, otherwise builds
// a live one from apiToken.
func (r *CloudflareZoneConfigReconciler) zoneClient(apiToken string) cfclient.ZoneClient {
	if r.ZoneClientFn != nil {
		return r.ZoneClientFn(apiToken)
	}
	return cfclient.NewZoneClientFromCF(cfclient.NewCloudflareClient(apiToken))
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareZoneConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareZoneConfig{}).
		Named("cloudflarezoneconfig").
		Complete(r)
}
