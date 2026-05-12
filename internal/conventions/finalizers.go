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
