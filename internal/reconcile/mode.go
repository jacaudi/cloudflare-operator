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
	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// ShouldMutate reports whether a reconciler should perform write operations
// on Cloudflare for a CR with the given mode. Used by CRDs that expose an
// observe/read-only mode (CloudflareDNSRecord in P5; future CRDs to follow).
//
// String-typed for reusability across CRDs regardless of their Mode enum
// type — the caller passes the raw string from spec.mode and the helper
// compares it against the canonical "Observe" sentinel value (bound to
// v1alpha1.RecordModeObserve so an enum rename breaks the build, not the
// gate). Empty input is treated as the default mutating mode (every CRD's
// Managed-equivalent).
//
// Future CRDs with their own Mode enum must use "Observe" as the read-only
// sentinel value so this helper stays applicable. If a different sentinel
// value is ever needed, add a sibling ShouldMutateWith(mode, sentinel)
// rather than overloading this function.
func ShouldMutate(mode string) bool {
	return mode != string(v1alpha1.RecordModeObserve)
}
