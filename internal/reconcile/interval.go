/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResolveInterval returns d.Duration when d is set to a strictly positive
// value, otherwise fallback. Centralizes the spec.interval-or-default idiom
// shared by every periodic reconciler. Not cmp.Or: the guard is ">0" on a
// *metav1.Duration, not first-non-zero.
func ResolveInterval(d *metav1.Duration, fallback time.Duration) time.Duration {
	if d != nil && d.Duration > 0 {
		return d.Duration
	}
	return fallback
}
