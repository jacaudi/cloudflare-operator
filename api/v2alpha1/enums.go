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
