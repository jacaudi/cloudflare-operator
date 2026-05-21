/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package conventions holds project-wide string constants and the canonical
// vocabulary for finalizers, labels, annotations, and status condition reasons.
//
// Per Foundation §6.1.1, finalizers.go and labels.go are owned in full by
// Foundation. annotations.go and conditions.go are append-only across specs:
// later specs add to them but never restructure.
package conventions

// FinalizerName is the finalizer added to every operator-managed CR.
// The specific prefix avoids collisions with external-dns / cert-manager /
// other operators that touch Cloudflare resources.
const FinalizerName = "cloudflare-operator.cloudflare.io/finalizer"
