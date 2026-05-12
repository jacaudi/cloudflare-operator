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
