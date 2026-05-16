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
