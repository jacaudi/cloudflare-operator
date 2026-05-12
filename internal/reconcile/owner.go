package reconcile

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// SetControllerOwner stamps a controller-style OwnerReference from owner onto
// child. Both blockOwnerDeletion and controller are true.
func SetControllerOwner(owner, child client.Object, scheme *runtime.Scheme) error {
	return controllerutil.SetControllerReference(owner, child, scheme)
}

// Compile-time assertion that we depend on runtime.Object via the scheme.
var _ runtime.Object = (client.Object)(nil)
