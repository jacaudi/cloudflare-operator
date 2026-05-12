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
