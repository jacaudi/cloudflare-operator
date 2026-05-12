// Package cloudflare interface declarations.
//
// This file is an append-only contract per Foundation §6.1.1. Spec 2 appends
// ZoneClient, DNSClient, RulesetClient, ZoneConfigClient. Spec 3 appends
// TunnelClient. Foundation creates the file empty so later specs have a
// canonical home for new interfaces; never restructure existing entries.

package cloudflare

// Sentinel — keep this file compiling when nothing else is appended yet.
type _interfacesContract struct{}
