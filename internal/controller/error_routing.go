/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"time"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrorRouting is the routing decision produced by ClassifyCloudflareError.
//
// Reconcilers use the fields as follows:
//   - Reason: passed to failReconcile (and optionally to a Recorder event)
//     as the condition reason.
//   - RequeueAfter: passed as the requeue duration. A zero value means
//     "use the reconciler's existing default" — preserves current behavior
//     for the catch-all case. ResetRemoteID==true overrides this: callers
//     should requeue immediately after clearing the stored ID.
//   - ResetRemoteID: when true, the reconciler MUST clear its stored
//     remote-ID status field before calling failReconcile. The helper
//     does not perform the write because the field name varies per CRD.
type ErrorRouting struct {
	Reason        string
	RequeueAfter  time.Duration
	ResetRemoteID bool
}

// ClassifyCloudflareError walks the predicate ladder (first match wins)
// and returns the routing decision for err. Predicate order is significant
// only between IsPlanTierRequired and IsPermissionDenied (both match 403);
// the plan-tier check MUST come first so plan-restricted failures get the
// distinct PlanTierRequired reason rather than the catch-all PermissionDenied.
//
// nil err returns the zero ErrorRouting{}; callers MUST gate on err != nil
// before calling. The zero value's empty Reason would write an invalid
// "" into the Ready condition if persisted.
func ClassifyCloudflareError(err error) ErrorRouting {
	switch {
	case err == nil:
		return ErrorRouting{}
	case cfclient.IsNotFound(err):
		return ErrorRouting{
			Reason:        cloudflarev1alpha1.ReasonRemoteGone,
			RequeueAfter:  0,
			ResetRemoteID: true,
		}
	case cfclient.IsBadRequest(err):
		return ErrorRouting{
			Reason:       cloudflarev1alpha1.ReasonInvalidSpec,
			RequeueAfter: time.Hour,
		}
	case cfclient.IsPlanTierRequired(err):
		return ErrorRouting{
			Reason:       cloudflarev1alpha1.ReasonPlanTierRequired,
			RequeueAfter: time.Hour,
		}
	case cfclient.IsPermissionDenied(err):
		return ErrorRouting{
			Reason:       cloudflarev1alpha1.ReasonPermissionDenied,
			RequeueAfter: time.Hour,
		}
	default:
		return ErrorRouting{
			Reason:       cloudflarev1alpha1.ReasonCloudflareError,
			RequeueAfter: 0, // caller falls back to its existing default
		}
	}
}
