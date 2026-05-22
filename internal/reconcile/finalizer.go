/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
// it was actually removed. The caller's existing finalizers slice is left
// untouched (copy-on-write); callers may safely retain a reference to it.
func RemoveFinalizer(obj client.Object, finalizer string) bool {
	fs := obj.GetFinalizers()
	out := slices.DeleteFunc(slices.Clone(fs), func(f string) bool { return f == finalizer })
	if len(out) == len(fs) {
		return false
	}
	obj.SetFinalizers(out)
	return true
}
