/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package cloudflare wraps the cloudflare-go SDK with the operator's
// credential resolution, error classification, and (SDK-built-in) retry
// semantics.
//
// interfaces.go is append-only across spec increments: spec 2 (zone bundle)
// shipped DNSClient / ZoneClient / RulesetClient / ZoneConfigClient; spec 3
// (tunnel bundle) appended TunnelClient. Future specs append; never remove
// or rename a published interface.
package cloudflare
