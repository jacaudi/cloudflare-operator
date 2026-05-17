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

package v2alpha1

import "testing"

// TestDomainCRDTypesExist verifies the five domain CRD types compile cleanly.
// Scheme registration is exercised in T4 once controller-gen has produced
// DeepCopyObject methods and api/v2alpha1/register.go is in place.
func TestDomainCRDTypesExist(t *testing.T) {
	var (
		_ CloudflareZone
		_ CloudflareZoneConfig
		_ CloudflareDNSRecord
		_ CloudflareRuleset
		_ CloudflareTunnel
	)
	_ = t
}
