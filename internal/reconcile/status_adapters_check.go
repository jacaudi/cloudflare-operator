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

import v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"

// Compile-time assertions that every Cloudflare*Status type satisfies the
// StatusEpilogue interface. Placed here (in internal/reconcile) rather than
// in api/v2alpha1 to avoid a circular import: internal/reconcile already
// imports api/v2alpha1, so the direction is legal; the reverse would cycle.
var (
	_ StatusEpilogue = (*v2alpha1.CloudflareZoneStatus)(nil)
	_ StatusEpilogue = (*v2alpha1.CloudflareZoneConfigStatus)(nil)
	_ StatusEpilogue = (*v2alpha1.CloudflareDNSRecordStatus)(nil)
	_ StatusEpilogue = (*v2alpha1.CloudflareRulesetStatus)(nil)
	_ StatusEpilogue = (*v2alpha1.CloudflareTunnelStatus)(nil)
)
