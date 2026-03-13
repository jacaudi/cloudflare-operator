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
	"fmt"
	"time"

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

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareZoneConfig resource. It handles applying zone settings to
// Cloudflare and tracking the applied settings count.
func (r *CloudflareZoneConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the CR
	var zoneConfig cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(ctx, req.NamespacedName, &zoneConfig); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

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
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, zoneConfig.Spec.SecretRef.Name, zoneConfig.Namespace)
	if err != nil {
		log.Error(err, "failed to get API token")
		status.SetReady(&zoneConfig.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonSecretNotFound, err.Error(), zoneConfig.Generation)
		if statusErr := r.Status().Update(ctx, &zoneConfig); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Build zone client
	var zoneClient cfclient.ZoneClient
	if r.ZoneClientFn != nil {
		zoneClient = r.ZoneClientFn(apiToken)
	} else {
		cfClient := cfclient.NewCloudflareClient(apiToken)
		zoneClient = cfclient.NewZoneClientFromCF(cfClient)
	}

	// 6. Reconcile the zone config
	result, err := r.reconcileZoneConfig(ctx, &zoneConfig, zoneClient)
	if err != nil {
		log.Error(err, "reconciliation failed")
		status.SetReady(&zoneConfig.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonCloudflareError, err.Error(), zoneConfig.Generation)
		r.Recorder.Event(&zoneConfig, "Warning", "SyncFailed", err.Error())
		if statusErr := r.Status().Update(ctx, &zoneConfig); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// 7. Update status
	zoneConfig.Status.ObservedGeneration = zoneConfig.Generation
	now := metav1.Now()
	zoneConfig.Status.LastSyncedAt = &now
	status.SetReady(&zoneConfig.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Zone config synced", zoneConfig.Generation)
	status.SetSynced(&zoneConfig.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Zone config synced", zoneConfig.Generation)
	if err := r.Status().Update(ctx, &zoneConfig); err != nil {
		return ctrl.Result{}, err
	}

	return result, nil
}

// settingUpdate represents a single zone setting to apply.
type settingUpdate struct {
	id    string
	value any
}

// collectSettings maps non-nil spec fields to Cloudflare setting IDs and values.
func collectSettings(spec *cloudflarev1alpha1.CloudflareZoneConfigSpec) []settingUpdate {
	var updates []settingUpdate

	if spec.SSL != nil {
		if spec.SSL.Mode != nil {
			updates = append(updates, settingUpdate{"ssl", *spec.SSL.Mode})
		}
		if spec.SSL.MinTLSVersion != nil {
			updates = append(updates, settingUpdate{"min_tls_version", *spec.SSL.MinTLSVersion})
		}
		if spec.SSL.TLS13 != nil {
			updates = append(updates, settingUpdate{"tls_1_3", *spec.SSL.TLS13})
		}
		if spec.SSL.AlwaysUseHTTPS != nil {
			updates = append(updates, settingUpdate{"always_use_https", *spec.SSL.AlwaysUseHTTPS})
		}
		if spec.SSL.AutomaticHTTPSRewrites != nil {
			updates = append(updates, settingUpdate{"automatic_https_rewrites", *spec.SSL.AutomaticHTTPSRewrites})
		}
		if spec.SSL.OpportunisticEncryption != nil {
			updates = append(updates, settingUpdate{"opportunistic_encryption", *spec.SSL.OpportunisticEncryption})
		}
	}

	if spec.Security != nil {
		if spec.Security.SecurityLevel != nil {
			updates = append(updates, settingUpdate{"security_level", *spec.Security.SecurityLevel})
		}
		if spec.Security.ChallengeTTL != nil {
			updates = append(updates, settingUpdate{"challenge_ttl", *spec.Security.ChallengeTTL})
		}
		if spec.Security.BrowserCheck != nil {
			updates = append(updates, settingUpdate{"browser_check", *spec.Security.BrowserCheck})
		}
		if spec.Security.EmailObfuscation != nil {
			updates = append(updates, settingUpdate{"email_obfuscation", *spec.Security.EmailObfuscation})
		}
	}

	if spec.Performance != nil {
		if spec.Performance.CacheLevel != nil {
			updates = append(updates, settingUpdate{"cache_level", *spec.Performance.CacheLevel})
		}
		if spec.Performance.BrowserCacheTTL != nil {
			updates = append(updates, settingUpdate{"browser_cache_ttl", *spec.Performance.BrowserCacheTTL})
		}
		if spec.Performance.Minify != nil {
			minifyValue := map[string]string{}
			if spec.Performance.Minify.CSS != nil {
				minifyValue["css"] = *spec.Performance.Minify.CSS
			}
			if spec.Performance.Minify.HTML != nil {
				minifyValue["html"] = *spec.Performance.Minify.HTML
			}
			if spec.Performance.Minify.JS != nil {
				minifyValue["js"] = *spec.Performance.Minify.JS
			}
			if len(minifyValue) > 0 {
				updates = append(updates, settingUpdate{"minify", minifyValue})
			}
		}
		if spec.Performance.Polish != nil {
			updates = append(updates, settingUpdate{"polish", *spec.Performance.Polish})
		}
		if spec.Performance.Brotli != nil {
			updates = append(updates, settingUpdate{"brotli", *spec.Performance.Brotli})
		}
		if spec.Performance.EarlyHints != nil {
			updates = append(updates, settingUpdate{"early_hints", *spec.Performance.EarlyHints})
		}
		if spec.Performance.HTTP2 != nil {
			updates = append(updates, settingUpdate{"http2", *spec.Performance.HTTP2})
		}
		if spec.Performance.HTTP3 != nil {
			updates = append(updates, settingUpdate{"http3", *spec.Performance.HTTP3})
		}
	}

	if spec.Network != nil {
		if spec.Network.IPv6 != nil {
			updates = append(updates, settingUpdate{"ipv6", *spec.Network.IPv6})
		}
		if spec.Network.WebSockets != nil {
			updates = append(updates, settingUpdate{"websockets", *spec.Network.WebSockets})
		}
		if spec.Network.PseudoIPv4 != nil {
			updates = append(updates, settingUpdate{"pseudo_ipv4", *spec.Network.PseudoIPv4})
		}
		if spec.Network.IPGeolocation != nil {
			updates = append(updates, settingUpdate{"ip_geolocation", *spec.Network.IPGeolocation})
		}
		if spec.Network.OpportunisticOnion != nil {
			updates = append(updates, settingUpdate{"opportunistic_onion", *spec.Network.OpportunisticOnion})
		}
	}

	return updates
}

func (r *CloudflareZoneConfigReconciler) reconcileZoneConfig(ctx context.Context, zoneConfig *cloudflarev1alpha1.CloudflareZoneConfig, zoneClient cfclient.ZoneClient) (ctrl.Result, error) {
	appliedCount := 0

	// Apply regular zone settings
	settings := collectSettings(&zoneConfig.Spec)
	for _, s := range settings {
		if err := zoneClient.UpdateSetting(ctx, zoneConfig.Spec.ZoneID, s.id, s.value); err != nil {
			return ctrl.Result{}, fmt.Errorf("update setting %s: %w", s.id, err)
		}
		appliedCount++
	}

	// Handle bot management separately (different API)
	if zoneConfig.Spec.BotManagement != nil {
		config := cfclient.BotManagementConfig{
			EnableJS:  zoneConfig.Spec.BotManagement.EnableJS,
			FightMode: zoneConfig.Spec.BotManagement.FightMode,
		}
		if err := zoneClient.UpdateBotManagement(ctx, zoneConfig.Spec.ZoneID, config); err != nil {
			return ctrl.Result{}, fmt.Errorf("update bot management: %w", err)
		}
		appliedCount++
	}

	zoneConfig.Status.AppliedSettings = appliedCount

	requeueAfter := 30 * time.Minute
	if zoneConfig.Spec.Interval != nil {
		requeueAfter = zoneConfig.Spec.Interval.Duration
	}

	r.Recorder.Event(zoneConfig, "Normal", "SettingsApplied",
		fmt.Sprintf("Applied %d settings to zone %s", appliedCount, zoneConfig.Spec.ZoneID))

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *CloudflareZoneConfigReconciler) reconcileDelete(ctx context.Context, zoneConfig *cloudflarev1alpha1.CloudflareZoneConfig) (ctrl.Result, error) {
	// Zone settings persist in Cloudflare — we don't revert on deletion
	controllerutil.RemoveFinalizer(zoneConfig, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, zoneConfig)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareZoneConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareZoneConfig{}).
		Named("cloudflarezoneconfig").
		Complete(r)
}
