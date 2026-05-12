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

import "sigs.k8s.io/controller-runtime/pkg/client"

// EnsureFinalizer adds the given finalizer if missing and reports whether
// the object was changed.
func EnsureFinalizer(obj client.Object, finalizer string) bool {
	for _, f := range obj.GetFinalizers() {
		if f == finalizer {
			return false
		}
	}
	obj.SetFinalizers(append(obj.GetFinalizers(), finalizer))
	return true
}

// RemoveFinalizer removes the given finalizer if present and reports whether
// the object was changed.
func RemoveFinalizer(obj client.Object, finalizer string) bool {
	fs := obj.GetFinalizers()
	out := fs[:0]
	changed := false
	for _, f := range fs {
		if f == finalizer {
			changed = true
			continue
		}
		out = append(out, f)
	}
	obj.SetFinalizers(out)
	return changed
}
