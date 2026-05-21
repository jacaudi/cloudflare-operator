/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

// Compile-time interface-satisfaction assertions. These assignments fail to
// compile the moment a concrete type stops implementing its interface — which
// is the real contract we need to protect, not a runtime check.
var (
	_ DNSClient        = (*dnsClient)(nil)
	_ ZoneClient       = (*zoneClient)(nil)
	_ ZoneConfigClient = (*zoneConfigClient)(nil)
	_ RulesetClient    = (*rulesetClient)(nil)
	_ TunnelClient     = (*tunnelClient)(nil)
)
