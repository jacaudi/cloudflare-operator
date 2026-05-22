/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package conventions

// Label keys for tracking the source object that originated a dog-fooded CR
// (e.g. a CloudflareDNSRecord emitted by spec 3's source reconcilers, owner-reffed
// back to a Service or HTTPRoute).
//
// Foundation §7 enforces a hard-fail policy: operator-emitted CRs MUST carry all
// three of these labels. The verifier in internal/reconcile/sourcelabels.go
// refuses to reconcile an operator-labeled CR with any of them missing.
const (
	LabelSourceKind      = "cloudflare.io/source-kind"
	LabelSourceName      = "cloudflare.io/source-name"
	LabelSourceNamespace = "cloudflare.io/source-namespace"
)
