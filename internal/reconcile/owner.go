/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

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
