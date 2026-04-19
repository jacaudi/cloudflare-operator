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
	"fmt"
	"reflect"
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
		logger.Error(err, "failed to resolve zone ID")
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err, 30*time.Second)
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, zoneConfig.Spec.SecretRef.Name, zoneConfig.Namespace)
	if err != nil {
		logger.Error(err, "failed to get API token")
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the zone config
	result, err := r.reconcileZoneConfig(ctx, &zoneConfig, r.zoneClient(apiToken), resolvedZoneID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		r.Recorder.Event(&zoneConfig, corev1.EventTypeWarning, "SyncFailed", err.Error())
		return failReconcile(ctx, r.Client, &zoneConfig, &zoneConfig.Status.Conditions,
			cloudflarev1alpha1.ReasonCloudflareError, err, time.Minute)
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

// collectSettings maps non-nil spec fields to Cloudflare setting IDs and values.
func collectSettings(spec *cloudflarev1alpha1.CloudflareZoneConfigSpec) []settingUpdate {
	var updates []settingUpdate
	updates = appendSSL(updates, spec.SSL)
	updates = appendSecurity(updates, spec.Security)
	updates = appendPerformance(updates, spec.Performance)
	updates = appendNetwork(updates, spec.Network)
	return updates
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

	// Apply regular zone settings
	settings := collectSettings(&zoneConfig.Spec)
	for _, s := range settings {
		if err := zoneClient.UpdateSetting(ctx, zoneID, s.id, s.value); err != nil {
			return ctrl.Result{}, fmt.Errorf("update setting %s: %w", s.id, err)
		}
	}
	appliedCount := len(settings)

	// Handle bot management separately (different API)
	if zoneConfig.Spec.BotManagement != nil {
		config := cfclient.BotManagementConfig{
			EnableJS:  zoneConfig.Spec.BotManagement.EnableJS,
			FightMode: zoneConfig.Spec.BotManagement.FightMode,
		}
		if err := zoneClient.UpdateBotManagement(ctx, zoneID, config); err != nil {
			return ctrl.Result{}, fmt.Errorf("update bot management: %w", err)
		}
		appliedCount++
	}

	zoneConfig.Status.AppliedSpecHash = desiredHash

	r.Recorder.Event(zoneConfig, corev1.EventTypeNormal, "SettingsApplied",
		fmt.Sprintf("Applied %d settings to zone %s", appliedCount, zoneID))

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
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
