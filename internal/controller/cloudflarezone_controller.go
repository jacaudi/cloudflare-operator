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
	"strings"
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

// CloudflareZoneReconciler reconciles a CloudflareZone object
type CloudflareZoneReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Recorder              record.EventRecorder
	ClientFactory         *cfclient.ClientFactory
	ZoneLifecycleClientFn func(apiToken string) cfclient.ZoneLifecycleClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *CloudflareZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the CR
	var zone cloudflarev1alpha1.CloudflareZone
	if err := r.Get(ctx, req.NamespacedName, &zone); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Handle deletion
	if !zone.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&zone, cloudflarev1alpha1.FinalizerName) {
			return r.reconcileDelete(ctx, &zone)
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&zone, cloudflarev1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&zone, cloudflarev1alpha1.FinalizerName)
		if err := r.Update(ctx, &zone); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, zone.Spec.SecretRef.Name, zone.Namespace)
	if err != nil {
		log.Error(err, "failed to get API token")
		status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonSecretNotFound, err.Error(), zone.Generation)
		if statusErr := r.Status().Update(ctx, &zone); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Build zone lifecycle client
	var zoneClient cfclient.ZoneLifecycleClient
	if r.ZoneLifecycleClientFn != nil {
		zoneClient = r.ZoneLifecycleClientFn(apiToken)
	} else {
		cfClient := cfclient.NewCloudflareClient(apiToken)
		zoneClient = cfclient.NewZoneLifecycleClientFromCF(cfClient)
	}

	// 6. Reconcile the zone
	result, err := r.reconcileZone(ctx, &zone, zoneClient)
	if err != nil {
		log.Error(err, "reconciliation failed")
		status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonCloudflareError, err.Error(), zone.Generation)
		r.Recorder.Event(&zone, "Warning", "SyncFailed", err.Error())
		if statusErr := r.Status().Update(ctx, &zone); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// 7. Update status
	zone.Status.ObservedGeneration = zone.Generation
	now := metav1.Now()
	zone.Status.LastSyncedAt = &now
	if err := r.Status().Update(ctx, &zone); err != nil {
		return ctrl.Result{}, err
	}

	return result, nil
}

func (r *CloudflareZoneReconciler) reconcileZone(ctx context.Context, zone *cloudflarev1alpha1.CloudflareZone, zoneClient cfclient.ZoneLifecycleClient) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Try to find zone by stored ID
	var existing *cfclient.Zone
	var err error
	if zone.Status.ZoneID != "" {
		existing, err = zoneClient.GetZone(ctx, zone.Status.ZoneID)
		if err != nil {
			log.Info("could not fetch zone by ID, will search by name", "zoneID", zone.Status.ZoneID)
			zone.Status.ZoneID = ""
			existing = nil
		}
	}

	// Search by name (adopt existing)
	if existing == nil {
		zones, err := zoneClient.ListZonesByName(ctx, zone.Spec.AccountID, zone.Spec.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("list zones: %w", err)
		}
		if len(zones) > 0 {
			existing = &zones[0]
			zone.Status.ZoneID = existing.ID
			log.Info("adopted existing zone", "zoneID", existing.ID)
			r.Recorder.Event(zone, "Normal", "ZoneAdopted",
				fmt.Sprintf("Adopted existing zone %s with ID %s", zone.Spec.Name, existing.ID))
		}
	}

	// Create if not found
	if existing == nil {
		created, err := zoneClient.CreateZone(ctx, zone.Spec.AccountID, cfclient.ZoneLifecycleParams{
			Name: zone.Spec.Name,
			Type: zone.Spec.Type,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create zone: %w", err)
		}
		existing = created
		zone.Status.ZoneID = created.ID
		log.Info("created zone", "zoneID", created.ID)
		r.Recorder.Event(zone, "Normal", "ZoneCreated",
			fmt.Sprintf("Created zone %s with ID %s", zone.Spec.Name, created.ID))
	}

	// Check if paused needs updating
	if zone.Spec.Paused != nil && *zone.Spec.Paused != existing.Paused {
		updated, err := zoneClient.EditZone(ctx, zone.Status.ZoneID, cfclient.ZoneLifecycleEditParams{
			Paused: zone.Spec.Paused,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("edit zone: %w", err)
		}
		existing = updated
		log.Info("updated zone paused state", "paused", *zone.Spec.Paused)
		r.Recorder.Event(zone, "Normal", "ZoneUpdated",
			fmt.Sprintf("Updated zone %s paused=%v", zone.Spec.Name, *zone.Spec.Paused))
	}

	// Update status fields from the zone response
	zone.Status.Status = existing.Status
	zone.Status.NameServers = existing.NameServers
	zone.Status.OriginalNameServers = existing.OriginalNameServers
	zone.Status.OriginalRegistrar = existing.OriginalRegistrar
	if existing.ActivatedOn != nil {
		t := metav1.NewTime(*existing.ActivatedOn)
		zone.Status.ActivatedOn = &t
	}

	// Set conditions and requeue interval based on zone status
	requeueAfter := 30 * time.Minute
	if zone.Spec.Interval != nil {
		requeueAfter = zone.Spec.Interval.Duration
	}

	switch existing.Status {
	case "active":
		status.SetReady(&zone.Status.Conditions, metav1.ConditionTrue,
			cloudflarev1alpha1.ReasonReconcileSuccess, "Zone is active", zone.Generation)
		status.SetSynced(&zone.Status.Conditions, metav1.ConditionTrue,
			cloudflarev1alpha1.ReasonReconcileSuccess, "Zone synced", zone.Generation)

	case "pending":
		// Trigger activation check
		if err := zoneClient.TriggerActivationCheck(ctx, zone.Status.ZoneID); err != nil {
			log.Error(err, "failed to trigger activation check")
		}

		nsMsg := fmt.Sprintf("Zone pending activation. Update your registrar NS records to: %s",
			strings.Join(existing.NameServers, ", "))
		status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonZonePending, nsMsg, zone.Generation)
		status.SetSynced(&zone.Status.Conditions, metav1.ConditionTrue,
			cloudflarev1alpha1.ReasonReconcileSuccess, "Zone synced", zone.Generation)

		// Shorter requeue when pending for faster activation detection
		if requeueAfter > 5*time.Minute {
			requeueAfter = 5 * time.Minute
		}

	default:
		status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonZoneNotActive,
			fmt.Sprintf("Zone status is %q", existing.Status), zone.Generation)
		status.SetSynced(&zone.Status.Conditions, metav1.ConditionTrue,
			cloudflarev1alpha1.ReasonReconcileSuccess, "Zone synced", zone.Generation)
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *CloudflareZoneReconciler) reconcileDelete(ctx context.Context, zone *cloudflarev1alpha1.CloudflareZone) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if zone.Spec.DeletionPolicy == "Delete" && zone.Status.ZoneID != "" {
		apiToken, err := r.ClientFactory.GetAPIToken(ctx, zone.Spec.SecretRef.Name, zone.Namespace)
		if err != nil {
			log.Error(err, "failed to get API token during deletion, will retry; remove the finalizer manually to force deletion")
			status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
				cloudflarev1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Cannot delete Cloudflare zone: %v. Remove the finalizer manually to force deletion.", err),
				zone.Generation)
			if statusErr := r.Status().Update(ctx, zone); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		var zoneClient cfclient.ZoneLifecycleClient
		if r.ZoneLifecycleClientFn != nil {
			zoneClient = r.ZoneLifecycleClientFn(apiToken)
		} else {
			cfClient := cfclient.NewCloudflareClient(apiToken)
			zoneClient = cfclient.NewZoneLifecycleClientFromCF(cfClient)
		}

		if err := zoneClient.DeleteZone(ctx, zone.Status.ZoneID); err != nil {
			log.Error(err, "failed to delete zone from Cloudflare")
			status.SetReady(&zone.Status.Conditions, metav1.ConditionFalse,
				cloudflarev1alpha1.ReasonCloudflareError, err.Error(), zone.Generation)
			if statusErr := r.Status().Update(ctx, zone); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.Info("deleted zone from Cloudflare", "zoneID", zone.Status.ZoneID)
		r.Recorder.Event(zone, "Normal", "ZoneDeleted",
			fmt.Sprintf("Deleted zone %s from Cloudflare", zone.Spec.Name))
	} else if zone.Status.ZoneID != "" {
		log.Info("retaining zone in Cloudflare per deletion policy", "zoneID", zone.Status.ZoneID)
		r.Recorder.Event(zone, "Normal", "ZoneRetained",
			fmt.Sprintf("Zone %s retained in Cloudflare (deletionPolicy=Retain)", zone.Spec.Name))
	}

	controllerutil.RemoveFinalizer(zone, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, zone)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareZone{}).
		Named("cloudflarezone").
		Complete(r)
}
