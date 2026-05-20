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
