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
	"encoding/json"
	stderrors "errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

// defaultRulesetInterval matches the CRD default on Spec.Interval
// (`+kubebuilder:default="30m"`); used as fallback when admission isn't in
// the loop (unit tests with the fake client).
const defaultRulesetInterval = 30 * time.Minute

// CloudflareRulesetReconciler drives the lifecycle of a CloudflareRuleset CR:
// credentials → resolve zone → build desired rule list → fetch existing phase
// entrypoint → upsert on first-write or drift → reflect status.
//
// Phase entrypoint rulesets are zone-scoped resources owned by the zone, not
// by this CR. On CR deletion we drop the finalizer but leave the entrypoint
// in place (deleting it would affect any other tooling touching the phase).
// Users who want to clear their rules should empty spec.rules, reconcile,
// then delete the CR.
type CloudflareRulesetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder is wired by the manager setup (T18). Nil is tolerated; event
	// emission no-ops without a recorder.
	Recorder record.EventRecorder
	// RulesetClientFn returns a Cloudflare RulesetClient for the resolved
	// credentials. Tests inject an in-memory mock.
	RulesetClientFn func(cloudflare.Credentials) (cloudflare.RulesetClient, error)
}

// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarerulesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarerulesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarerulesets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one iteration of the CloudflareRuleset state machine.
func (r *CloudflareRulesetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cloudflareruleset", req.NamespacedName)

	var rs v1alpha1.CloudflareRuleset
	if err := r.Get(ctx, req.NamespacedName, &rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path: leave the upstream phase entrypoint in place; only drop
	// the finalizer.
	if !rs.DeletionTimestamp.IsZero() {
		if reconcile.RemoveFinalizer(&rs, conventions.FinalizerName) {
			if err := r.Update(ctx, &rs); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if reconcile.EnsureFinalizer(&rs, conventions.FinalizerName) {
		if err := r.Update(ctx, &rs); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, rs.Spec.Cloudflare, rs.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if halt != nil {
		return reconcile.HaltCredentialsUnavailable(ctx, r.Client, &rs, &rs.Status.Conditions, &rs.Status.Phase, halt)
	}

	// Snapshot status so the trailing Status().Update can be skipped when
	// nothing material changed; LastSyncedAt/ObservedGeneration are masked.
	originalStatus := rs.Status.DeepCopy()

	rc, err := r.RulesetClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	zres, err := reconcile.ResolveZoneID(ctx, r.Client, reconcile.ZoneRefInputs{
		ZoneID: rs.Spec.ZoneID, ZoneRef: rs.Spec.ZoneRef,
	}, rs.Namespace)
	if err != nil {
		if stderrors.Is(err, reconcile.ErrZoneRefNotFound) {
			return r.haltDependency(ctx, &rs, err.Error())
		}
		return ctrl.Result{}, err
	}
	if zres.ZoneID == "" {
		return r.haltDependency(ctx, &rs, "zoneRef target has no status.zoneID yet")
	}
	zoneID := zres.ZoneID

	desiredRules, err := specToCloudflareRules(rs.Spec.Rules)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build rules: %w", err)
	}

	params := cloudflare.RulesetParams{
		Name:        rs.Spec.Name,
		Description: rs.Spec.Description,
		Phase:       rs.Spec.Phase,
		Rules:       desiredRules,
	}

	// Fetch the current phase entrypoint. ErrPhaseEntrypointNotFound is the
	// "first write" sentinel — go straight to Upsert without a drift check.
	existing, err := rc.GetPhaseEntrypoint(ctx, zoneID, rs.Spec.Phase)
	switch {
	case stderrors.Is(err, cloudflare.ErrPhaseEntrypointNotFound):
		existing = nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get phase entrypoint: %w", err)
	}

	switch {
	case existing == nil:
		created, cerr := rc.UpsertPhaseEntrypoint(ctx, zoneID, rs.Spec.Phase, params)
		if cerr != nil {
			return ctrl.Result{}, fmt.Errorf("upsert phase entrypoint: %w", cerr)
		}
		rs.Status.RulesetID = created.ID
		rs.Status.RuleCount = len(created.Rules)
		logger.Info("created phase entrypoint", "phase", rs.Spec.Phase, "rulesetID", created.ID)
		if r.Recorder != nil {
			r.Recorder.Eventf(&rs, corev1.EventTypeNormal, conventions.ReasonReconciling,
				"created %s phase entrypoint %s", rs.Spec.Phase, created.ID)
		}

	case rulesetMatches(existing, params):
		// In sync — no API write needed.
		rs.Status.RulesetID = existing.ID
		rs.Status.RuleCount = len(existing.Rules)

	default:
		updated, uerr := rc.UpsertPhaseEntrypoint(ctx, zoneID, rs.Spec.Phase, params)
		if uerr != nil {
			return ctrl.Result{}, fmt.Errorf("upsert phase entrypoint: %w", uerr)
		}
		rs.Status.RulesetID = updated.ID
		rs.Status.RuleCount = len(updated.Rules)
		logger.Info("updated phase entrypoint", "phase", rs.Spec.Phase, "rulesetID", updated.ID)
		if r.Recorder != nil {
			r.Recorder.Eventf(&rs, corev1.EventTypeNormal, conventions.ReasonDriftDetected,
				"updated %s phase entrypoint %s", rs.Spec.Phase, updated.ID)
		}
	}

	rs.Status.Conditions = reconcile.SetReady(rs.Status.Conditions, metav1.ConditionTrue,
		conventions.ReasonReady, "ruleset synced")
	rs.Status.Phase = reconcile.DerivePhase(metav1.ConditionTrue, conventions.ReasonReady)

	candidate := rs.Status.DeepCopy()
	candidate.LastSyncedAt = originalStatus.LastSyncedAt
	candidate.ObservedGeneration = originalStatus.ObservedGeneration
	if rs.Generation != originalStatus.ObservedGeneration || !equality.Semantic.DeepEqual(originalStatus, candidate) {
		now := metav1.Now()
		rs.Status.LastSyncedAt = &now
		rs.Status.ObservedGeneration = rs.Generation
		if err := r.Status().Update(ctx, &rs); err != nil {
			return ctrl.Result{}, err
		}
	}

	interval := defaultRulesetInterval
	if rs.Spec.Interval != nil && rs.Spec.Interval.Duration > 0 {
		interval = rs.Spec.Interval.Duration
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// haltDependency persists a DependencyMissing Ready=False and requeues; used
// when the referenced CloudflareZone isn't ready yet.
func (r *CloudflareRulesetReconciler) haltDependency(ctx context.Context, rs *v1alpha1.CloudflareRuleset, msg string) (ctrl.Result, error) {
	return reconcile.HaltDependency(ctx, r.Client, rs, &rs.Status.Conditions, &rs.Status.Phase, msg, reconcile.DefaultRequeueAfter)
}

// specToCloudflareRules converts CRD-side RulesetRuleSpec slices to the
// internal cloudflare.RulesetRule type used by the client.
//
// Logging normalization: when Logging.Enabled is explicitly false, drop the
// Logging field entirely. The cloudflare-go SDK's response shape can't
// distinguish enabled=false from "no logging block", so the read-back path
// in internal/cloudflare/ruleset.go always returns Logging=nil for that case.
// Mirroring the normalization here ensures rulesetMatches compares equal and
// avoids a permanent reconcile loop on explicit-disable inputs. To enable
// logging, set Enabled=true.
func specToCloudflareRules(specRules []v1alpha1.RulesetRuleSpec) ([]cloudflare.RulesetRule, error) {
	rules := make([]cloudflare.RulesetRule, 0, len(specRules))
	for _, sr := range specRules {
		rule := cloudflare.RulesetRule{
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
		if sr.Logging != nil && sr.Logging.Enabled != nil && *sr.Logging.Enabled {
			// Only carry Logging when explicitly true; false/nil collapse to
			// "unset" so the API round-trip is idempotent.
			t := true
			rule.Logging = &cloudflare.RuleLogging{Enabled: &t}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// rulesetMatches reports whether the live ruleset already matches the desired
// params. Rule IDs are ignored (Cloudflare-assigned). ActionParameters are
// compared via reflect.DeepEqual after both sides have been normalized to
// map[string]any. Logging is compared via ruleLoggingEqual after applying the
// same false→nil normalization the write path uses, so explicit-disable
// inputs converge on the first reconcile.
func rulesetMatches(existing *cloudflare.Ruleset, desired cloudflare.RulesetParams) bool {
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
		if !ruleLoggingEqual(normalizeLogging(got.Logging), normalizeLogging(want.Logging)) {
			return false
		}
	}
	return true
}

// normalizeLogging collapses Logging.Enabled=false to nil so it compares
// equal to the read-back path's "no logging block" representation. Returns
// the input unchanged when Enabled is nil or true.
func normalizeLogging(l *cloudflare.RuleLogging) *cloudflare.RuleLogging {
	if l == nil {
		return nil
	}
	if l.Enabled != nil && !*l.Enabled {
		return nil
	}
	return l
}

// ruleLoggingEqual compares two normalized *RuleLogging values structurally.
// Callers must pass normalizeLogging-processed inputs.
func ruleLoggingEqual(a, b *cloudflare.RuleLogging) bool {
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
