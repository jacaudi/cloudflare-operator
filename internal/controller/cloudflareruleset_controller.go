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
	"encoding/json"
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

// CloudflareRulesetReconciler reconciles a CloudflareRuleset object
type CloudflareRulesetReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	ClientFactory   *cfclient.ClientFactory
	RulesetClientFn func(apiToken string) cfclient.RulesetClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarerulesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarerulesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarerulesets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareRuleset resource. It handles the full lifecycle of rulesets
// including creation, updates, adoption of existing rulesets, and deletion.
func (r *CloudflareRulesetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the CR
	var ruleset cloudflarev1alpha1.CloudflareRuleset
	if err := r.Get(ctx, req.NamespacedName, &ruleset); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Handle deletion
	if !ruleset.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&ruleset, cloudflarev1alpha1.FinalizerName) {
			return r.reconcileDelete(ctx, &ruleset)
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&ruleset, cloudflarev1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&ruleset, cloudflarev1alpha1.FinalizerName)
		if err := r.Update(ctx, &ruleset); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 3.5. Resolve zone ID
	resolvedZoneID, err := ResolveZoneID(ctx, r.Client, ruleset.Namespace, ruleset.Spec.ZoneID, ruleset.Spec.ZoneRef)
	if err != nil {
		log.Error(err, "failed to resolve zone ID")
		status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err.Error(), ruleset.Generation)
		if statusErr := r.Status().Update(ctx, &ruleset); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, ruleset.Spec.SecretRef.Name, ruleset.Namespace)
	if err != nil {
		log.Error(err, "failed to get API token")
		status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonSecretNotFound, err.Error(), ruleset.Generation)
		if statusErr := r.Status().Update(ctx, &ruleset); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Build ruleset client
	var rulesetClient cfclient.RulesetClient
	if r.RulesetClientFn != nil {
		rulesetClient = r.RulesetClientFn(apiToken)
	} else {
		cfClient := cfclient.NewCloudflareClient(apiToken)
		rulesetClient = cfclient.NewRulesetClientFromCF(cfClient)
	}

	// 6. Reconcile the ruleset
	result, err := r.reconcileRuleset(ctx, &ruleset, rulesetClient, resolvedZoneID)
	if err != nil {
		log.Error(err, "reconciliation failed")
		status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonCloudflareError, err.Error(), ruleset.Generation)
		r.Recorder.Event(&ruleset, "Warning", "SyncFailed", err.Error())
		if statusErr := r.Status().Update(ctx, &ruleset); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// 7. Update status
	ruleset.Status.ObservedGeneration = ruleset.Generation
	now := metav1.Now()
	ruleset.Status.LastSyncedAt = &now
	status.SetReady(&ruleset.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Ruleset synced", ruleset.Generation)
	status.SetSynced(&ruleset.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Ruleset synced", ruleset.Generation)
	if err := r.Status().Update(ctx, &ruleset); err != nil {
		return ctrl.Result{}, err
	}

	return result, nil
}

func (r *CloudflareRulesetReconciler) reconcileRuleset(ctx context.Context, ruleset *cloudflarev1alpha1.CloudflareRuleset, rulesetClient cfclient.RulesetClient, zoneID string) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Build desired rules from spec
	desiredRules, err := r.buildRules(ruleset.Spec.Rules)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build rules: %w", err)
	}

	params := cfclient.RulesetParams{
		Name:        ruleset.Spec.Name,
		Description: ruleset.Spec.Description,
		Phase:       ruleset.Spec.Phase,
		Rules:       desiredRules,
	}

	// Check if ruleset exists by ID
	var existing *cfclient.Ruleset
	if ruleset.Status.RulesetID != "" {
		existing, err = rulesetClient.GetRuleset(ctx, zoneID, ruleset.Status.RulesetID)
		if err != nil {
			log.Info("could not fetch ruleset by ID, will search by phase", "rulesetID", ruleset.Status.RulesetID)
			ruleset.Status.RulesetID = ""
			existing = nil
		}
	}

	// Search by phase (adopt existing)
	if existing == nil {
		rulesets, err := rulesetClient.ListRulesetsByPhase(ctx, zoneID, ruleset.Spec.Phase)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("list rulesets: %w", err)
		}
		if len(rulesets) > 0 {
			existing = &rulesets[0]
			ruleset.Status.RulesetID = existing.ID
			log.Info("adopted existing ruleset", "rulesetID", existing.ID)
			r.Recorder.Event(ruleset, "Normal", "RulesetAdopted",
				fmt.Sprintf("Adopted existing ruleset %s", existing.ID))
		}
	}

	requeueAfter := 30 * time.Minute
	if ruleset.Spec.Interval != nil {
		requeueAfter = ruleset.Spec.Interval.Duration
	}

	if existing == nil {
		// Create new ruleset
		created, err := rulesetClient.CreateRuleset(ctx, zoneID, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create ruleset: %w", err)
		}
		ruleset.Status.RulesetID = created.ID
		ruleset.Status.RuleCount = len(created.Rules)
		log.Info("created ruleset", "rulesetID", created.ID)
		r.Recorder.Event(ruleset, "Normal", "RulesetCreated",
			fmt.Sprintf("Created ruleset %s with ID %s", ruleset.Spec.Name, created.ID))
	} else {
		// Always update (PUT replaces all rules atomically — idempotent)
		updated, err := rulesetClient.UpdateRuleset(ctx, zoneID, existing.ID, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("update ruleset: %w", err)
		}
		ruleset.Status.RuleCount = len(updated.Rules)
		log.Info("updated ruleset", "rulesetID", existing.ID)
		r.Recorder.Event(ruleset, "Normal", "RulesetUpdated",
			fmt.Sprintf("Updated ruleset %s", existing.ID))
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// buildRules converts CRD RulesetRuleSpec slices to internal RulesetRule slices.
func (r *CloudflareRulesetReconciler) buildRules(specRules []cloudflarev1alpha1.RulesetRuleSpec) ([]cfclient.RulesetRule, error) {
	rules := make([]cfclient.RulesetRule, 0, len(specRules))
	for _, sr := range specRules {
		rule := cfclient.RulesetRule{
			Action:      sr.Action,
			Expression:  sr.Expression,
			Description: sr.Description,
		}
		if sr.Enabled != nil {
			rule.Enabled = *sr.Enabled
		} else {
			rule.Enabled = true
		}
		if sr.ActionParameters != nil {
			var m map[string]any
			if err := json.Unmarshal(sr.ActionParameters.Raw, &m); err != nil {
				return nil, fmt.Errorf("unmarshal actionParameters for rule %q: %w", sr.Description, err)
			}
			rule.ActionParameters = m
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (r *CloudflareRulesetReconciler) reconcileDelete(ctx context.Context, ruleset *cloudflarev1alpha1.CloudflareRuleset) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if ruleset.Status.RulesetID != "" {
		resolvedZoneID, err := ResolveZoneID(ctx, r.Client, ruleset.Namespace, ruleset.Spec.ZoneID, ruleset.Spec.ZoneRef)
		if err != nil {
			log.Error(err, "failed to resolve zone ID during deletion, will retry; remove the finalizer manually to force deletion")
			status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
				cloudflarev1alpha1.ReasonZoneRefNotReady,
				fmt.Sprintf("Cannot delete Cloudflare resource: %v. Remove the finalizer manually to force deletion.", err),
				ruleset.Generation)
			if statusErr := r.Status().Update(ctx, ruleset); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		apiToken, err := r.ClientFactory.GetAPIToken(ctx, ruleset.Spec.SecretRef.Name, ruleset.Namespace)
		if err != nil {
			log.Error(err, "failed to get API token during deletion, will retry; remove the finalizer manually to force deletion")
			status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
				cloudflarev1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Cannot delete Cloudflare resource: %v. Remove the finalizer manually to force deletion.", err),
				ruleset.Generation)
			if statusErr := r.Status().Update(ctx, ruleset); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		} else {
			var rulesetClient cfclient.RulesetClient
			if r.RulesetClientFn != nil {
				rulesetClient = r.RulesetClientFn(apiToken)
			} else {
				cfClient := cfclient.NewCloudflareClient(apiToken)
				rulesetClient = cfclient.NewRulesetClientFromCF(cfClient)
			}

			if err := rulesetClient.DeleteRuleset(ctx, resolvedZoneID, ruleset.Status.RulesetID); err != nil {
				log.Error(err, "failed to delete ruleset from Cloudflare")
				status.SetReady(&ruleset.Status.Conditions, metav1.ConditionFalse,
					cloudflarev1alpha1.ReasonCloudflareError, err.Error(), ruleset.Generation)
				if statusErr := r.Status().Update(ctx, ruleset); statusErr != nil {
					log.Error(statusErr, "failed to update status")
				}
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			log.Info("deleted ruleset from Cloudflare", "rulesetID", ruleset.Status.RulesetID)
			r.Recorder.Event(ruleset, "Normal", "RulesetDeleted",
				fmt.Sprintf("Deleted ruleset %s from Cloudflare", ruleset.Spec.Name))
		}
	}

	controllerutil.RemoveFinalizer(ruleset, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, ruleset)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareRulesetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareRuleset{}).
		Named("cloudflareruleset").
		Complete(r)
}
