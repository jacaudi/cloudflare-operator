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
	preStatus := ruleset.Status.DeepCopy()

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
	resolvedZoneID, err := ResolveZoneID(ctx, r.Client, &ruleset)
	if err != nil {
		log.Error(err, "failed to resolve zone ID")
		return failReconcile(ctx, r.Client, &ruleset, &ruleset.Status.Conditions,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err, 30*time.Second)
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, ruleset.Spec.SecretRef.Name, ruleset.Namespace)
	if err != nil {
		log.Error(err, "failed to get API token")
		return failReconcile(ctx, r.Client, &ruleset, &ruleset.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the ruleset
	result, err := r.reconcileRuleset(ctx, &ruleset, r.rulesetClient(apiToken), resolvedZoneID)
	if err != nil {
		log.Error(err, "reconciliation failed")
		r.Recorder.Event(&ruleset, corev1.EventTypeWarning, "SyncFailed", err.Error())
		return failReconcile(ctx, r.Client, &ruleset, &ruleset.Status.Conditions,
			cloudflarev1alpha1.ReasonCloudflareError, err, time.Minute)
	}

	// 7. Persist status only if anything materially changed.
	ruleset.Status.ObservedGeneration = ruleset.Generation
	status.SetReady(&ruleset.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Ruleset synced", ruleset.Generation)
	if !reflect.DeepEqual(preStatus, &ruleset.Status) {
		now := metav1.Now()
		ruleset.Status.LastSyncedAt = &now
		if err := r.Status().Update(ctx, &ruleset); err != nil {
			return ctrl.Result{}, err
		}
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
			r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetAdopted",
				fmt.Sprintf("Adopted existing ruleset %s", existing.ID))
		}
	}

	requeueAfter := 30 * time.Minute
	if ruleset.Spec.Interval != nil {
		requeueAfter = ruleset.Spec.Interval.Duration
	}

	switch {
	case existing == nil:
		// Create new ruleset
		created, err := rulesetClient.CreateRuleset(ctx, zoneID, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create ruleset: %w", err)
		}
		ruleset.Status.RulesetID = created.ID
		ruleset.Status.RuleCount = len(created.Rules)
		log.Info("created ruleset", "rulesetID", created.ID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetCreated",
			fmt.Sprintf("Created ruleset %s with ID %s", ruleset.Spec.Name, created.ID))

	case rulesetMatches(existing, params):
		// In sync — skip the PUT to avoid API churn.
		ruleset.Status.RuleCount = len(existing.Rules)

	default:
		// PUT replaces all rules atomically.
		updated, err := rulesetClient.UpdateRuleset(ctx, zoneID, existing.ID, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("update ruleset: %w", err)
		}
		ruleset.Status.RuleCount = len(updated.Rules)
		log.Info("updated ruleset", "rulesetID", existing.ID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetUpdated",
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
		resolvedZoneID, err := ResolveZoneID(ctx, r.Client, ruleset)
		if err != nil {
			log.Error(err, "failed to resolve zone ID during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, ruleset, &ruleset.Status.Conditions,
				cloudflarev1alpha1.ReasonZoneRefNotReady, wrapDeleteErr(err), 30*time.Second)
		}

		apiToken, err := r.ClientFactory.GetAPIToken(ctx, ruleset.Spec.SecretRef.Name, ruleset.Namespace)
		if err != nil {
			log.Error(err, "failed to get API token during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, ruleset, &ruleset.Status.Conditions,
				cloudflarev1alpha1.ReasonSecretNotFound, wrapDeleteErr(err), 30*time.Second)
		}

		if err := r.rulesetClient(apiToken).DeleteRuleset(ctx, resolvedZoneID, ruleset.Status.RulesetID); err != nil {
			log.Error(err, "failed to delete ruleset from Cloudflare")
			return failReconcile(ctx, r.Client, ruleset, &ruleset.Status.Conditions,
				cloudflarev1alpha1.ReasonCloudflareError, err, 30*time.Second)
		}
		log.Info("deleted ruleset from Cloudflare", "rulesetID", ruleset.Status.RulesetID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetDeleted",
			fmt.Sprintf("Deleted ruleset %s from Cloudflare", ruleset.Spec.Name))
	}

	controllerutil.RemoveFinalizer(ruleset, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, ruleset)
}

// rulesetMatches reports whether the live ruleset already matches the desired
// params. Rule IDs are ignored (Cloudflare-assigned, not part of desired state).
// ActionParameters are compared via reflect.DeepEqual after both sides have
// been normalized to map[string]any through JSON on their respective read paths,
// so value shapes match when semantically equal.
func rulesetMatches(existing *cfclient.Ruleset, desired cfclient.RulesetParams) bool {
	if existing.Name != desired.Name ||
		existing.Description != desired.Description ||
		len(existing.Rules) != len(desired.Rules) {
		return false
	}
	for i, want := range desired.Rules {
		got := existing.Rules[i]
		if got.Action != want.Action ||
			got.Expression != want.Expression ||
			got.Description != want.Description ||
			got.Enabled != want.Enabled {
			return false
		}
		if !reflect.DeepEqual(got.ActionParameters, want.ActionParameters) {
			return false
		}
	}
	return true
}

// rulesetClient returns the test-injected RulesetClient if present, otherwise
// builds a live one from apiToken.
func (r *CloudflareRulesetReconciler) rulesetClient(apiToken string) cfclient.RulesetClient {
	if r.RulesetClientFn != nil {
		return r.RulesetClientFn(apiToken)
	}
	return cfclient.NewRulesetClientFromCF(cfclient.NewCloudflareClient(apiToken))
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareRulesetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareRuleset{}).
		Named("cloudflareruleset").
		Complete(r)
}
