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
