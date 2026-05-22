/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
