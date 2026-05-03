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
	stderrors "errors"
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
	ClientFactory   CredentialFactory
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
	logger := log.FromContext(ctx)

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
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 3.5. Resolve zone ID
	resolvedZoneID, err := ResolveZoneID(ctx, r.Client, &ruleset)
	if err != nil {
		if stderrors.Is(err, ErrZoneRefNotReady) {
			logger.Info("waiting for zone reference", "error", err.Error())
		} else {
			logger.Error(err, "failed to resolve zone ID")
		}
		return failReconcile(ctx, r.Client, &ruleset, &ruleset.Status.Conditions,
			cloudflarev1alpha1.ReasonZoneRefNotReady, err, 30*time.Second)
	}

	// 4. Get API token
	secretNs := secretRefNamespace(ruleset.Spec.SecretRef, ruleset.Namespace)
	creds, halt, err := LoadCredentials(ctx, r.Client, r.ClientFactory,
		ruleset.Spec.SecretRef.Name, secretNs,
		r.Recorder, &ruleset, &ruleset.Status.Conditions, 30*time.Second)
	if halt != nil {
		if err == nil {
			logger.V(1).Info("credential load failed; halting reconcile",
				"secret", ruleset.Spec.SecretRef.Name, "namespace", secretNs)
		} else {
			logger.Error(err, "credential load failed")
		}
		return *halt, err
	}
	apiToken := creds.APIToken

	// 5. Reconcile the ruleset
	result, err := r.reconcileRuleset(ctx, &ruleset, r.rulesetClient(apiToken), resolvedZoneID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		routing := ClassifyCloudflareError(err)
		// Note: IsNotFound and IsPlanTierRequired do not occur on this code path
		// in practice (no remote-ID-driven GETs; no plan-tier-restricted endpoints).
		// The classifier handles them generically if they ever surface.
		eventReason := routing.Reason
		if eventReason == cloudflarev1alpha1.ReasonCloudflareError {
			eventReason = "SyncFailed" // preserve historical event name for unclassified failures
		}
		r.Recorder.Event(&ruleset, corev1.EventTypeWarning, eventReason, err.Error())
		requeue := routing.RequeueAfter
		// requeue==0 means either: immediate (RemoteGone, with ResetRemoteID true)
		// or "use my default" (catch-all, with ResetRemoteID false).
		if requeue == 0 && !routing.ResetRemoteID {
			requeue = time.Minute
		}
		return failReconcile(ctx, r.Client, &ruleset, &ruleset.Status.Conditions,
			routing.Reason, err, requeue)
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
	logger := log.FromContext(ctx)

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

	// Fetch the current phase entrypoint ruleset. Cloudflare scopes one per
	// phase per zone; it may or may not exist yet.
	existing, err := rulesetClient.GetPhaseEntrypoint(ctx, zoneID, ruleset.Spec.Phase)
	switch {
	case stderrors.Is(err, cfclient.ErrPhaseEntrypointNotFound):
		// First apply for this phase: Upsert below will create the entrypoint.
		existing = nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get phase entrypoint: %w", err)
	}

	requeueAfter := 30 * time.Minute
	if ruleset.Spec.Interval != nil {
		requeueAfter = ruleset.Spec.Interval.Duration
	}

	switch {
	case existing == nil:
		// No entrypoint yet — Upsert creates it.
		created, err := rulesetClient.UpsertPhaseEntrypoint(ctx, zoneID, ruleset.Spec.Phase, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert phase entrypoint: %w", err)
		}
		ruleset.Status.RulesetID = created.ID
		ruleset.Status.RuleCount = len(created.Rules)
		logger.Info("created phase entrypoint ruleset", "phase", ruleset.Spec.Phase, "rulesetID", created.ID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetCreated",
			fmt.Sprintf("Created %s phase entrypoint with ID %s", ruleset.Spec.Phase, created.ID))

	case rulesetMatches(existing, params):
		// In sync — skip the PUT to avoid API churn.
		ruleset.Status.RulesetID = existing.ID
		ruleset.Status.RuleCount = len(existing.Rules)

	default:
		// Drift detected (including first adoption of a pre-existing entrypoint
		// whose rules differ from spec). Upsert replaces all rules atomically.
		if ruleset.Status.RulesetID == "" {
			logger.Info("adopted existing phase entrypoint, updating rules",
				"phase", ruleset.Spec.Phase, "rulesetID", existing.ID)
			r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetAdopted",
				fmt.Sprintf("Adopted existing %s phase entrypoint %s", ruleset.Spec.Phase, existing.ID))
		}
		updated, err := rulesetClient.UpsertPhaseEntrypoint(ctx, zoneID, ruleset.Spec.Phase, params)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert phase entrypoint: %w", err)
		}
		ruleset.Status.RulesetID = updated.ID
		ruleset.Status.RuleCount = len(updated.Rules)
		logger.Info("updated phase entrypoint ruleset", "phase", ruleset.Spec.Phase, "rulesetID", updated.ID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetUpdated",
			fmt.Sprintf("Updated %s phase entrypoint %s", ruleset.Spec.Phase, updated.ID))
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
		if sr.Logging != nil {
			rule.Logging = &cfclient.RuleLogging{Enabled: sr.Logging.Enabled}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (r *CloudflareRulesetReconciler) reconcileDelete(ctx context.Context, ruleset *cloudflarev1alpha1.CloudflareRuleset) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Phase entrypoint rulesets are zone-scoped resources — one per phase per
	// zone — owned by the zone itself, not by this CR. Deleting them entirely
	// is destructive and would affect any other tooling touching the phase.
	// Instead, on CR delete we leave the entrypoint in place. Users who want
	// to clear their rules should empty spec.rules, wait for reconciliation,
	// then delete the CR.
	if ruleset.Status.RulesetID != "" {
		logger.Info("leaving phase entrypoint in Cloudflare on CR deletion (entrypoints are zone-owned)",
			"phase", ruleset.Spec.Phase, "rulesetID", ruleset.Status.RulesetID)
		r.Recorder.Event(ruleset, corev1.EventTypeNormal, "RulesetRetained",
			fmt.Sprintf("Phase entrypoint %s retained in Cloudflare on CR deletion", ruleset.Status.RulesetID))
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
		if !ruleLoggingEqual(got.Logging, want.Logging) {
			return false
		}
	}
	return true
}

// ruleLoggingEqual compares two *RuleLogging values structurally.
// nil == nil; nil != non-nil; otherwise compare Enabled pointer dereferences.
func ruleLoggingEqual(a, b *cfclient.RuleLogging) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if (a.Enabled == nil) != (b.Enabled == nil) {
		return false
	}
	if a.Enabled != nil && *a.Enabled != *b.Enabled {
		return false
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
