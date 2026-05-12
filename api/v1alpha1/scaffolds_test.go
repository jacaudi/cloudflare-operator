package v1alpha1

import "testing"

// TestDomainCRDTypesExist verifies the five domain CRD types compile cleanly.
// Scheme registration is exercised in T4 once controller-gen has produced
// DeepCopyObject methods and api/v1alpha1/register.go is in place.
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
