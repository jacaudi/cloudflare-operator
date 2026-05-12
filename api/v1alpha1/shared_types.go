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

package v1alpha1

import "fmt"

// SecretReference identifies a Kubernetes Secret carrying credentials.
type SecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. Defaults to the referencing CR's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key inside the Secret holding the Cloudflare API token. Defaults to "token".
	// +kubebuilder:default=token
	// +optional
	Key string `json:"key,omitempty"`
}

// IsEmpty reports whether the reference has no name set.
func (r SecretReference) IsEmpty() bool {
	return r.Name == ""
}

// CloudflareCredentialRef bundles the credential Secret and account ID.
// Per Foundation §5 these are inherited or overridden as a unit.
type CloudflareCredentialRef struct {
	// TokenSecretRef points at the Secret carrying the Cloudflare API token.
	TokenSecretRef SecretReference `json:"tokenSecretRef"`

	// AccountID is the Cloudflare account ID this credential scopes to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	AccountID string `json:"accountID"`

	// TxtRegistryKeySecretRef references a Secret containing an AES-256 key
	// used to encrypt the TXT-registry ownership records. Only meaningful on
	// the top-level CloudflareOperator; ignored on per-CR overrides. When
	// unset, the TXT registry runs in plaintext mode (encryption is opt-in).
	// +optional
	TxtRegistryKeySecretRef *SecretReference `json:"txtRegistryKeySecretRef,omitempty"`
}

// Phase is reserved as the schema seat for the coarse-grained status summary
// derived from the Ready condition. Specs 2/3 add `Phase` to each CRD's
// status; Foundation declares the type and constants only.
// +kubebuilder:validation:Enum=Ready;Reconciling;Error;Pending
type Phase string

const (
	PhaseReady       Phase = "Ready"
	PhaseReconciling Phase = "Reconciling"
	PhaseError       Phase = "Error"
	PhasePending     Phase = "Pending"
)

// ZoneReference selects a CloudflareZone CR by name (and optional namespace).
// Used XOR with a literal zoneID per Foundation §7.
type ZoneReference struct {
	// Name of the CloudflareZone CR.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the CloudflareZone CR. Defaults to the referencing CR's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// Validate returns an error if the reference is structurally empty.
func (r ZoneReference) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("zoneRef.name must be set")
	}
	return nil
}
