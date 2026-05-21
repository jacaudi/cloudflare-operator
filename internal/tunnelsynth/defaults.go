/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnelsynth

import (
	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// DefaultsFor derives synth Defaults from a CloudflareTunnel CR's Spec.
// Returns a zero-value Defaults when tn (or its Routing / OriginRequest)
// is nil. Values are copied by value so callers cannot accidentally
// mutate the underlying CR.
func DefaultsFor(tn *v2alpha1.CloudflareTunnel) Defaults {
	if tn == nil || tn.Spec.Routing == nil || tn.Spec.Routing.OriginRequest == nil {
		return Defaults{}
	}
	or := tn.Spec.Routing.OriginRequest
	d := Defaults{}
	if or.NoTLSVerify != nil {
		v := *or.NoTLSVerify
		d.NoTLSVerifyDefault = &v
	}
	if or.OriginServerName != nil {
		v := *or.OriginServerName
		d.OriginServerNameDefault = &v
	}
	return d
}
