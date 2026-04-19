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
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// CloudflareDNSRecordReconciler reconciles a CloudflareDNSRecord object
type CloudflareDNSRecordReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Recorder      record.EventRecorder
	ClientFactory *cfclient.ClientFactory
	IPResolver    *ipresolver.Resolver
	DNSClientFn   func(apiToken string) cfclient.DNSClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareDNSRecord resource. It handles the full lifecycle of DNS records
// including creation, updates, adoption of existing records, and deletion.
func (r *CloudflareDNSRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the CR
	var dnsRecord cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(ctx, req.NamespacedName, &dnsRecord); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	preStatus := dnsRecord.Status.DeepCopy()

	// 2. Handle deletion
	if !dnsRecord.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&dnsRecord, cloudflarev1alpha1.FinalizerName) {
			return r.reconcileDelete(ctx, &dnsRecord)
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&dnsRecord, cloudflarev1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&dnsRecord, cloudflarev1alpha1.FinalizerName)
		if err := r.Update(ctx, &dnsRecord); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 3.5. Resolve zone ID
	resolvedZoneID, err := ResolveZoneID(ctx, r.Client, &dnsRecord)
	if err != nil {
		logger.Error(err, "failed to resolve zone ID")
		return failReconcile(ctx, r.Client, &dnsRecord, &dnsRecord.Status.Conditions,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err, 30*time.Second)
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, dnsRecord.Spec.SecretRef.Name, dnsRecord.Namespace)
	if err != nil {
		logger.Error(err, "failed to get API token")
		return failReconcile(ctx, r.Client, &dnsRecord, &dnsRecord.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the DNS record
	result, err := r.reconcileRecord(ctx, &dnsRecord, r.dnsClient(apiToken), resolvedZoneID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		r.Recorder.Event(&dnsRecord, corev1.EventTypeWarning, "SyncFailed", err.Error())
		return failReconcile(ctx, r.Client, &dnsRecord, &dnsRecord.Status.Conditions,
			cloudflarev1alpha1.ReasonCloudflareError, err, time.Minute)
	}

	// 7. Persist status only if anything materially changed. LastSyncedAt is
	// bumped only on a real write to keep it meaningful as a liveness signal.
	dnsRecord.Status.ObservedGeneration = dnsRecord.Generation
	status.SetReady(&dnsRecord.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "DNS record synced", dnsRecord.Generation)
	if !reflect.DeepEqual(preStatus, &dnsRecord.Status) {
		now := metav1.Now()
		dnsRecord.Status.LastSyncedAt = &now
		if err := r.Status().Update(ctx, &dnsRecord); err != nil {
			return ctrl.Result{}, err
		}
	}

	return result, nil
}

func (r *CloudflareDNSRecordReconciler) reconcileRecord(ctx context.Context, dnsRecord *cloudflarev1alpha1.CloudflareDNSRecord, dnsClient cfclient.DNSClient, zoneID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine desired content
	desiredContent, err := r.resolveContent(ctx, dnsRecord)
	if err != nil {
		return failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions,
			cloudflarev1alpha1.ReasonIPResolutionError, err, time.Minute)
	}

	// Check if record exists by ID
	var existing *cfclient.DNSRecord
	if dnsRecord.Status.RecordID != "" {
		existing, err = dnsClient.GetRecord(ctx, zoneID, dnsRecord.Status.RecordID)
		if err != nil {
			logger.Info("could not fetch record by ID, will search by name", "recordID", dnsRecord.Status.RecordID)
			dnsRecord.Status.RecordID = ""
			existing = nil
		}
	}

	// Search by name + type (adopt existing)
	if existing == nil {
		records, err := dnsClient.ListRecordsByNameAndType(ctx, zoneID, dnsRecord.Spec.Name, dnsRecord.Spec.Type)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("list records: %w", err)
		}
		if len(records) > 0 {
			existing = &records[0]
			dnsRecord.Status.RecordID = existing.ID
			logger.Info("adopted existing DNS record", "recordID", existing.ID)
			r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, "RecordAdopted",
				fmt.Sprintf("Adopted existing DNS record %s", existing.ID))
		}
	}

	// Build desired params
	params := cfclient.DNSRecordParams{
		Name:    dnsRecord.Spec.Name,
		Type:    dnsRecord.Spec.Type,
		Content: desiredContent,
		TTL:     dnsRecord.Spec.TTL,
		Proxied: dnsRecord.Spec.Proxied,
	}
	if dnsRecord.Spec.Priority != nil {
		params.Priority = dnsRecord.Spec.Priority
	}
	if dnsRecord.Spec.SRVData != nil {
		params.Data = map[string]any{
			"service":  dnsRecord.Spec.SRVData.Service,
			"proto":    dnsRecord.Spec.SRVData.Proto,
			"name":     dnsRecord.Spec.Name,
			"priority": dnsRecord.Spec.SRVData.Priority,
			"weight":   dnsRecord.Spec.SRVData.Weight,
			"port":     dnsRecord.Spec.SRVData.Port,
			"target":   dnsRecord.Spec.SRVData.Target,
		}
	}

	requeueAfter := 5 * time.Minute
	if dnsRecord.Spec.Interval != nil {
		requeueAfter = dnsRecord.Spec.Interval.Duration
	}

	if existing == nil {
		// Create
		created, err := dnsClient.CreateRecord(ctx, zoneID, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create record: %w", err)
		}
		dnsRecord.Status.RecordID = created.ID
		dnsRecord.Status.CurrentContent = created.Content
		logger.Info("created DNS record", "recordID", created.ID)
		r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, "RecordCreated",
			fmt.Sprintf("Created DNS record %s -> %s", dnsRecord.Spec.Name, created.Content))
	} else {
		// Check if update needed
		if r.needsUpdate(existing, params) {
			updated, err := dnsClient.UpdateRecord(ctx, zoneID, existing.ID, params)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("update record: %w", err)
			}
			dnsRecord.Status.CurrentContent = updated.Content
			logger.Info("updated DNS record", "recordID", existing.ID)
			r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, "RecordUpdated",
				fmt.Sprintf("Updated DNS record %s: %s -> %s", dnsRecord.Spec.Name, existing.Content, updated.Content))
		} else {
			dnsRecord.Status.CurrentContent = existing.Content
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *CloudflareDNSRecordReconciler) resolveContent(ctx context.Context, dnsRecord *cloudflarev1alpha1.CloudflareDNSRecord) (string, error) {
	if dnsRecord.Spec.DynamicIP {
		if dnsRecord.Spec.Type != cloudflarev1alpha1.DNSRecordTypeA {
			return "", fmt.Errorf("dynamicIP is only valid for type A records")
		}
		return r.IPResolver.GetExternalIP(ctx)
	}
	if dnsRecord.Spec.Type == cloudflarev1alpha1.DNSRecordTypeSRV {
		return "", nil
	}
	if dnsRecord.Spec.Content == nil {
		return "", fmt.Errorf("content is required when dynamicIP is false")
	}
	return *dnsRecord.Spec.Content, nil
}

func (r *CloudflareDNSRecordReconciler) needsUpdate(existing *cfclient.DNSRecord, desired cfclient.DNSRecordParams) bool {
	if existing.Content != desired.Content && desired.Content != "" {
		return true
	}
	if desired.Proxied != nil && existing.Proxied != *desired.Proxied {
		return true
	}
	if desired.TTL > 0 && existing.TTL != desired.TTL {
		return true
	}
	return false
}

func (r *CloudflareDNSRecordReconciler) reconcileDelete(ctx context.Context, dnsRecord *cloudflarev1alpha1.CloudflareDNSRecord) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if dnsRecord.Status.RecordID != "" {
		resolvedZoneID, err := ResolveZoneID(ctx, r.Client, dnsRecord)
		if err != nil {
			logger.Error(err, "failed to resolve zone ID during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions,
				cloudflarev1alpha1.ReasonZoneRefNotReady, wrapDeleteErr(err), 30*time.Second)
		}

		apiToken, err := r.ClientFactory.GetAPIToken(ctx, dnsRecord.Spec.SecretRef.Name, dnsRecord.Namespace)
		if err != nil {
			logger.Error(err, "failed to get API token during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions,
				cloudflarev1alpha1.ReasonSecretNotFound, wrapDeleteErr(err), 30*time.Second)
		}

		if err := r.dnsClient(apiToken).DeleteRecord(ctx, resolvedZoneID, dnsRecord.Status.RecordID); err != nil {
			logger.Error(err, "failed to delete DNS record from Cloudflare")
			return failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions,
				cloudflarev1alpha1.ReasonCloudflareError, err, 30*time.Second)
		}
		logger.Info("deleted DNS record from Cloudflare", "recordID", dnsRecord.Status.RecordID)
		r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, "RecordDeleted",
			fmt.Sprintf("Deleted DNS record %s from Cloudflare", dnsRecord.Spec.Name))
	}

	controllerutil.RemoveFinalizer(dnsRecord, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, dnsRecord)
}

// dnsClient returns the test-injected DNSClient if present, otherwise builds
// a live one from apiToken.
func (r *CloudflareDNSRecordReconciler) dnsClient(apiToken string) cfclient.DNSClient {
	if r.DNSClientFn != nil {
		return r.DNSClientFn(apiToken)
	}
	return cfclient.NewDNSClientFromCF(cfclient.NewCloudflareClient(apiToken))
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareDNSRecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareDNSRecord{}).
		Named("cloudflarednsrecord").
		Complete(r)
}
