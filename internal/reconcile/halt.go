/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// HaltDependency persists a DependencyMissing Ready=False condition and
// requeues. It is the shared form of the per-controller helper that the
// CloudflareZoneConfig, CloudflareDNSRecord, and CloudflareRuleset
// reconcilers each previously duplicated.
//
// Callers pass pointers to the CR's Conditions slice and Phase field; the
// helper writes through the pointers, persists status, and returns a
// requeue Result. Callers should return (result, nil) on success.
//
// The requeueAfter argument lets callers preserve their previous interval
// choice (30s for zoneconfig literal, DefaultRequeueAfter — also 30s — for
// dnsrecord/ruleset). Passing 0 falls back to DefaultRequeueAfter.
func HaltDependency(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	conds *[]metav1.Condition,
	phase *v2alpha1.Phase,
	msg string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	*conds = SetReady(*conds, metav1.ConditionFalse, conventions.ReasonDependencyMissing, msg)
	*phase = DerivePhase(metav1.ConditionFalse, conventions.ReasonDependencyMissing)
	if err := c.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}
	after := requeueAfter
	if after <= 0 {
		after = DefaultRequeueAfter
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// HaltWith sets a Ready=False condition with the supplied reason and message,
// writes status, and returns a halt-and-requeue result. It is a generalized
// sibling to HaltDependency / HaltCredentialsUnavailable: callers supply the
// reason + message directly. Passing requeueAfter ≤ 0 falls back to
// DefaultRequeueAfter.
func HaltWith(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	conds *[]metav1.Condition,
	phase *v2alpha1.Phase,
	reason, message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	*conds = SetReady(*conds, metav1.ConditionFalse, reason, message)
	*phase = DerivePhase(metav1.ConditionFalse, reason)
	if err := c.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}
	after := requeueAfter
	if after <= 0 {
		after = DefaultRequeueAfter
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// HaltCredentialsUnavailable persists a CredentialsUnavailable Ready=False
// condition and returns the halt result produced by LoadCredentials /
// LoadCredentialsHierarchical. It is the shared form of the post-credential-
// halt block that the zone, zoneconfig, dnsrecord, ruleset, and tunnel
// reconcilers each previously duplicated.
//
// Callers pass pointers to the CR's Conditions slice and Phase field plus the
// non-nil *ctrl.Result returned by the credential loader; the helper writes
// through the pointers, persists status, and returns (*halt, nil) on success.
func HaltCredentialsUnavailable(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	conds *[]metav1.Condition,
	phase *v2alpha1.Phase,
	halt *ctrl.Result,
) (ctrl.Result, error) {
	*conds = SetReady(*conds, metav1.ConditionFalse,
		conventions.ReasonCredentialsUnavailable, "cloudflare credentials unavailable")
	*phase = DerivePhase(metav1.ConditionFalse, conventions.ReasonCredentialsUnavailable)
	if err := c.Status().Update(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}
	return *halt, nil
}
