package v1alpha1

// DNS record type constants (mirrors the kubebuilder enum on CloudflareDNSRecordSpec.Type).
const (
	DNSRecordTypeA     = "A"
	DNSRecordTypeAAAA  = "AAAA"
	DNSRecordTypeCNAME = "CNAME"
	DNSRecordTypeSRV   = "SRV"
	DNSRecordTypeMX    = "MX"
	DNSRecordTypeTXT   = "TXT"
	DNSRecordTypeNS    = "NS"
)

// Zone status values returned by the Cloudflare API.
const (
	ZoneStatusInitializing = "initializing"
	ZoneStatusPending      = "pending"
	ZoneStatusActive       = "active"
	ZoneStatusMoved        = "moved"
)

// DeletionPolicy values for CloudflareZone.Spec.DeletionPolicy.
const (
	DeletionPolicyRetain = "Retain"
	DeletionPolicyDelete = "Delete"
)
