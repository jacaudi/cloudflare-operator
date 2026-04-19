package v1alpha1

// GetZoneID returns the inline Cloudflare zone ID (may be empty).
func (r *CloudflareDNSRecord) GetZoneID() string { return r.Spec.ZoneID }

// GetZoneRef returns the optional reference to a CloudflareZone CR.
func (r *CloudflareDNSRecord) GetZoneRef() *ZoneReference { return r.Spec.ZoneRef }

// GetZoneID returns the inline Cloudflare zone ID (may be empty).
func (r *CloudflareRuleset) GetZoneID() string { return r.Spec.ZoneID }

// GetZoneRef returns the optional reference to a CloudflareZone CR.
func (r *CloudflareRuleset) GetZoneRef() *ZoneReference { return r.Spec.ZoneRef }

// GetZoneID returns the inline Cloudflare zone ID (may be empty).
func (r *CloudflareZoneConfig) GetZoneID() string { return r.Spec.ZoneID }

// GetZoneRef returns the optional reference to a CloudflareZone CR.
func (r *CloudflareZoneConfig) GetZoneRef() *ZoneReference { return r.Spec.ZoneRef }
