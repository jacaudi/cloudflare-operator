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
	stderrors "errors"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// ErrUnownedDeployment is returned when reconcileConnectorResources finds an
// existing Deployment that is not owned by the reconciled CloudflareTunnel.
// Tests MUST use errors.Is(err, ErrUnownedDeployment) to assert this case.
var ErrUnownedDeployment = stderrors.New("refusing to adopt Deployment not owned by this tunnel")

// ReconcileConnectorAndRules performs the Task 8 additions to the tunnel
// reconcile: aggregates CloudflareTunnelRule CRs for this tunnel, renders
// config.yaml, reconciles the connector workload (when enabled), and writes
// per-rule + per-tunnel status.
//
// Pure controller-runtime operation: no Cloudflare API calls. Called by
// Reconcile after tunnel provisioning has populated TunnelID/TunnelCNAME.
// preStatus is the status snapshot taken before Reconcile began; it is used
// by writeTunnelAggStatus to set LastSyncedAt only when status actually changed.
func ReconcileConnectorAndRules(ctx context.Context, c client.Client, tun *cloudflarev1alpha1.CloudflareTunnel, preStatus *cloudflarev1alpha1.CloudflareTunnelStatus) error {
	var ruleList cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := c.List(ctx, &ruleList); err != nil {
		return fmt.Errorf("list CloudflareTunnelRule: %w", err)
	}
	filtered := filterRulesForTunnel(ruleList.Items, tun.Name, tun.Namespace)

	if tun.Status.TunnelID == "" {
		return fmt.Errorf("render connector config: tunnel ID is empty on %s/%s", tun.Namespace, tun.Name)
	}
	agg := Aggregate(tun.Status.TunnelID, filtered, tun.Spec.Routing)

	if tun.Spec.Connector != nil && tun.Spec.Connector.Enabled {
		if err := reconcileConnectorResources(ctx, c, tun, agg); err != nil {
			return err
		}
	}

	for i := range filtered {
		r := &filtered[i]
		k := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
		decision, ok := agg.Decisions[k]
		if !ok {
			continue
		}
		if err := writeRuleStatus(ctx, c, r, decision, agg.ConfigHash); err != nil {
			return fmt.Errorf("rule %s status: %w", k, err)
		}
	}

	return writeTunnelAggStatus(ctx, c, tun, agg, preStatus)
}

// filterRulesForTunnel returns only the rules whose TunnelRef resolves to
// the given (tunnelName, tunnelNs) pair. When TunnelRef.Namespace is empty it
// defaults to the rule's own namespace.
func filterRulesForTunnel(all []cloudflarev1alpha1.CloudflareTunnelRule, tunnelName, tunnelNs string) []cloudflarev1alpha1.CloudflareTunnelRule {
	var out []cloudflarev1alpha1.CloudflareTunnelRule
	for _, r := range all {
		ref := r.Spec.TunnelRef
		ns := ref.Namespace
		if ns == "" {
			ns = r.Namespace
		}
		if ref.Name == tunnelName && ns == tunnelNs {
			out = append(out, r)
		}
	}
	return out
}

// reconcileConnectorResources reconciles the ServiceAccount, ConfigMap, and
// Deployment for the operator-managed cloudflared workload.
//
// The Deployment update path uses retry.RetryOnConflict to absorb transient
// optimistic-concurrency conflicts (kube-side metadata bumps between our
// Get and Update). Without this, sustained conflicts propagate as reconcile
// errors, the controller workqueue rate-limiter applies exponential backoff
// up to ~16 minutes, and downstream events (e.g. CR deletion) sit behind
// that backoff until the operator pod is restarted (#59).
func reconcileConnectorResources(ctx context.Context, c client.Client, tun *cloudflarev1alpha1.CloudflareTunnel, agg AggregationResult) error {
	sa := BuildConnectorServiceAccount(tun)
	if err := applyOwned(ctx, c, sa, &corev1.ServiceAccount{}); err != nil {
		return fmt.Errorf("apply ServiceAccount: %w", err)
	}

	cm := BuildConnectorConfigMap(tun, agg.Rendered, agg.ConfigHash)
	if err := applyOwned(ctx, c, cm, &corev1.ConfigMap{}); err != nil {
		return fmt.Errorf("apply ConfigMap: %w", err)
	}

	desired := BuildConnectorDeployment(tun, agg.ConfigHash)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var existing appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing)
		switch {
		case errors.IsNotFound(err):
			return c.Create(ctx, desired)
		case err != nil:
			return fmt.Errorf("get deployment: %w", err)
		default:
			if !isOwnedBy(existing.OwnerReferences, tun.UID) {
				return fmt.Errorf("%w: %s/%s", ErrUnownedDeployment, existing.Namespace, existing.Name)
			}
			// applyOwned semantics: wholesale overwrite labels, annotations, and
			// ownerRefs. Build* functions produce a complete, known label set so
			// overwriting is correct — there are no operator-external labels to
			// preserve on these operator-owned resources.
			existing.Spec = desired.Spec
			existing.Labels = desired.Labels
			existing.Annotations = desired.Annotations
			existing.OwnerReferences = desired.OwnerReferences
			return c.Update(ctx, &existing)
		}
	})
}

// applyOwned creates or updates a fully operator-owned resource (SA or
// ConfigMap). On create: submit as-is. On update: wholesale-overwrite
// labels, annotations, ownerRefs, and the data fields that Build*
// functions set, with retry.RetryOnConflict to absorb transient
// optimistic-concurrency conflicts (#59).
func applyOwned(ctx context.Context, c client.Client, desired client.Object, existing client.Object) error {
	key := types.NamespacedName{Namespace: desired.GetNamespace(), Name: desired.GetName()}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := c.Get(ctx, key, existing)
		switch {
		case errors.IsNotFound(err):
			return c.Create(ctx, desired)
		case err != nil:
			return fmt.Errorf("get %T: %w", existing, err)
		}

		// Wholesale overwrite metadata that Build* controls, then type-specific fields.
		existing.SetLabels(desired.GetLabels())
		existing.SetAnnotations(desired.GetAnnotations())
		existing.SetOwnerReferences(desired.GetOwnerReferences())

		switch dst := existing.(type) {
		case *corev1.ConfigMap:
			src := desired.(*corev1.ConfigMap)
			dst.Data = src.Data
		case *corev1.ServiceAccount:
			// ServiceAccount has no operator-controlled data fields beyond metadata.
		}
		return c.Update(ctx, existing)
	})
}

// isOwnedBy reports whether ownerRefs contains a reference with the given UID.
func isOwnedBy(ownerRefs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range ownerRefs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

// writeRuleStatus writes the per-rule status conditions (Valid, TunnelAccepted,
// Conflict) based on the aggregation decision. Uses r.Generation for
// ObservedGeneration — not the tunnel's generation.
// configHash is the aggregation result config hash; it is set on
// AppliedToConfigHash only for included rules.
func writeRuleStatus(ctx context.Context, c client.Client, r *cloudflarev1alpha1.CloudflareTunnelRule, decision RuleDecision, configHash string) error {
	conds := r.Status.Conditions

	switch decision.Status {
	case RuleIncluded:
		// Valid=True, TunnelAccepted=True, Conflict=False.
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeValid,
			metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess, "rule is valid", r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeTunnelAccepted,
			metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess, "rule included in tunnel config", r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeConflict,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconcileSuccess, "no conflict", r.Generation)

	case RuleDuplicateHostname:
		// Valid=True, TunnelAccepted=False, Conflict=True.
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeValid,
			metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess, "rule is valid", r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeTunnelAccepted,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonDuplicateHostname, decision.Message, r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeConflict,
			metav1.ConditionTrue, cloudflarev1alpha1.ReasonDuplicateHostname, decision.Message, r.Generation)

	case RuleInvalid:
		// Valid=False, TunnelAccepted=False, Conflict=False.
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeValid,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonInvalidSpec, decision.Message, r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeTunnelAccepted,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonInvalidSpec, decision.Message, r.Generation)
		status.SetCondition(&conds, cloudflarev1alpha1.ConditionTypeConflict,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconcileSuccess, "no conflict", r.Generation)
	}

	r.Status.Conditions = conds
	r.Status.ObservedGeneration = r.Generation
	if decision.Status == RuleIncluded {
		r.Status.ResolvedBackend = decision.ResolvedBackend
		r.Status.AppliedToConfigHash = configHash
	} else {
		r.Status.ResolvedBackend = ""
		r.Status.AppliedToConfigHash = ""
	}

	return c.Status().Update(ctx, r)
}

// writeTunnelAggStatus writes IngressConfigured, ConnectorReady (or removes
// it when connector is disabled), and the ConnectorStatus sub-status.
// preStatus is the snapshot taken at the start of Reconcile; if the status
// has changed it also sets LastSyncedAt so callers don't need a second write.
func writeTunnelAggStatus(ctx context.Context, c client.Client, tun *cloudflarev1alpha1.CloudflareTunnel, agg AggregationResult, preStatus *cloudflarev1alpha1.CloudflareTunnelStatus) error {
	included := countIncluded(agg.Decisions)
	msg := fmt.Sprintf("%d rules configured in tunnel ingress", included)
	status.SetCondition(&tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeIngressConfigured,
		metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess, msg, tun.Generation)

	if tun.Spec.Connector != nil && tun.Spec.Connector.Enabled {
		if err := writeTunnelConnectorStatus(ctx, c, tun, agg); err != nil {
			return err
		}
	} else {
		// Connector disabled: remove ConnectorReady condition and nil the sub-status.
		meta.RemoveStatusCondition(&tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady)
		tun.Status.Connector = nil
	}

	// Set LastSyncedAt when status actually changed (mirrors the check in Reconcile).
	if preStatus != nil && !reflect.DeepEqual(preStatus, &tun.Status) {
		now := metav1.Now()
		tun.Status.LastSyncedAt = &now
	}

	return c.Status().Update(ctx, tun)
}

// writeTunnelConnectorStatus fetches the live Deployment and writes the
// ConnectorReady condition and ConnectorStatus sub-status.
//
// ConnectorStatus fields are populated per-case:
//   - Replicas and ConfigHash are always set (from spec and agg respectively),
//     independent of whether the Deployment lookup succeeds.
//   - ReadyReplicas and Image are set from the live Deployment when found.
func writeTunnelConnectorStatus(ctx context.Context, c client.Client, tun *cloudflarev1alpha1.CloudflareTunnel, agg AggregationResult) error {
	n := ConnectorNames(tun)
	desired := BuildConnectorDeployment(tun, agg.ConfigHash)

	cs := &cloudflarev1alpha1.ConnectorStatus{
		Replicas:   *desired.Spec.Replicas,
		ConfigHash: agg.ConfigHash,
	}

	var dep appsv1.Deployment
	err := c.Get(ctx, types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep)
	switch {
	case errors.IsNotFound(err):
		// Deployment not yet created (first reconcile creates it in
		// reconcileConnectorResources before we reach here). Report Reconciling.
		status.SetCondition(&tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
			metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconciling, "connector Deployment not yet available", tun.Generation)
	case err != nil:
		return fmt.Errorf("get connector deployment: %w", err)
	default:
		cs.ReadyReplicas = dep.Status.ReadyReplicas
		if len(dep.Spec.Template.Spec.Containers) > 0 {
			cs.Image = dep.Spec.Template.Spec.Containers[0].Image
		}
		if dep.Status.ReadyReplicas >= 1 {
			status.SetCondition(&tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
				metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess,
				fmt.Sprintf("%d/%d replicas ready", dep.Status.ReadyReplicas, *dep.Spec.Replicas), tun.Generation)
		} else {
			status.SetCondition(&tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
				metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconciling,
				fmt.Sprintf("0/%d replicas ready", *dep.Spec.Replicas), tun.Generation)
		}
	}

	tun.Status.Connector = cs
	return nil
}

// countIncluded returns the number of RuleIncluded decisions in agg.
func countIncluded(decisions map[types.NamespacedName]RuleDecision) int {
	n := 0
	for _, d := range decisions {
		if d.Status == RuleIncluded {
			n++
		}
	}
	return n
}
