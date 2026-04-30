// internal/cloudflare/interfaces.go
package cloudflare

import (
	"context"
	"time"
)

// DNSRecord represents a Cloudflare DNS record.
type DNSRecord struct {
	ID      string
	Name    string
	Type    string
	Content string
	Proxied bool
	TTL     int
	Data    map[string]any
}

// DNSRecordParams are parameters for creating/updating a DNS record.
type DNSRecordParams struct {
	Name     string
	Type     string
	Content  string
	Proxied  *bool
	TTL      int
	Priority *int
	Data     map[string]any
}

// DNSClient manages Cloudflare DNS records.
type DNSClient interface {
	GetRecord(ctx context.Context, zoneID, recordID string) (*DNSRecord, error)
	ListRecordsByNameAndType(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, zoneID string, params DNSRecordParams) (*DNSRecord, error)
	UpdateRecord(ctx context.Context, zoneID, recordID string, params DNSRecordParams) (*DNSRecord, error)
	DeleteRecord(ctx context.Context, zoneID, recordID string) error
}

// Tunnel represents a Cloudflare Tunnel.
type Tunnel struct {
	ID   string
	Name string
}

// TunnelParams are parameters for creating a tunnel.
type TunnelParams struct {
	Name         string
	TunnelSecret string
}

// TunnelClient manages Cloudflare Tunnels.
type TunnelClient interface {
	GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error)
	ListTunnelsByName(ctx context.Context, accountID, name string) ([]Tunnel, error)
	CreateTunnel(ctx context.Context, accountID string, params TunnelParams) (*Tunnel, error)
	DeleteTunnel(ctx context.Context, accountID, tunnelID string) error
}

// Ruleset represents a Cloudflare Ruleset.
type Ruleset struct {
	ID          string
	Name        string
	Description string
	Phase       string
	Rules       []RulesetRule
}

// RuleLogging configures per-rule logging behavior.
// Pointer Enabled so callers can distinguish "unset" (nil) from "set to false".
type RuleLogging struct {
	Enabled *bool
}

// RulesetRule is a single rule in a ruleset.
type RulesetRule struct {
	ID               string
	Action           string
	Expression       string
	Description      string
	Enabled          bool
	ActionParameters map[string]any
	Logging          *RuleLogging
}

// RulesetParams are parameters for creating/updating a ruleset.
type RulesetParams struct {
	Name        string
	Description string
	Phase       string
	Rules       []RulesetRule
}

// RulesetClient manages a zone's phase-entrypoint rulesets.
//
// Cloudflare has two ruleset kinds: "zone" (the phase entrypoint — one per
// phase per zone, what the dashboard surfaces as Security rules / Custom
// rules / Rate limiting rules / etc.) and "custom" (standalone rulesets, a
// Business+ feature). The operator manages the phase entrypoint so it works
// on all plans.
type RulesetClient interface {
	// GetPhaseEntrypoint returns the zone's entrypoint ruleset for the given
	// phase. Returns ErrPhaseEntrypointNotFound when the entrypoint has not
	// been created yet (no Update has ever been made for that phase on this
	// zone). Any other error indicates an API / transport failure.
	GetPhaseEntrypoint(ctx context.Context, zoneID, phase string) (*Ruleset, error)

	// UpsertPhaseEntrypoint writes the given rules to the zone's entrypoint
	// ruleset for the given phase. Creates the entrypoint if it does not
	// already exist, otherwise replaces its rule set.
	UpsertPhaseEntrypoint(ctx context.Context, zoneID, phase string, params RulesetParams) (*Ruleset, error)
}

// ZoneSetting is a key-value pair for a zone setting.
type ZoneSetting struct {
	ID    string
	Value any
}

// BotManagementConfig represents bot management settings.
// Pointer fields allow distinguishing between "unset" and "set to false".
type BotManagementConfig struct {
	EnableJS  *bool
	FightMode *bool
}

// ZoneClient manages Cloudflare Zone settings and bot management.
type ZoneClient interface {
	GetSettings(ctx context.Context, zoneID string) ([]ZoneSetting, error)
	UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error
	GetBotManagement(ctx context.Context, zoneID string) (*BotManagementConfig, error)
	UpdateBotManagement(ctx context.Context, zoneID string, config BotManagementConfig) error
}

// Zone represents a Cloudflare Zone (lifecycle information).
type Zone struct {
	ID                  string
	Name                string
	Status              string // initializing, pending, active, moved
	Type                string // full, partial, secondary
	Paused              bool
	NameServers         []string
	OriginalNameServers []string
	OriginalRegistrar   string
	VerificationKey     string
	ActivatedOn         *time.Time
}

// ZoneLifecycleParams are parameters for creating a zone.
type ZoneLifecycleParams struct {
	Name string
	Type string // full, partial, secondary
}

// ZoneLifecycleEditParams are parameters for editing a zone.
type ZoneLifecycleEditParams struct {
	Paused *bool
}

// ZoneLifecycleClient manages Cloudflare Zone lifecycle (create/get/list/edit/delete).
type ZoneLifecycleClient interface {
	CreateZone(ctx context.Context, accountID string, params ZoneLifecycleParams) (*Zone, error)
	GetZone(ctx context.Context, zoneID string) (*Zone, error)
	ListZonesByName(ctx context.Context, accountID, name string) ([]Zone, error)
	EditZone(ctx context.Context, zoneID string, params ZoneLifecycleEditParams) (*Zone, error)
	DeleteZone(ctx context.Context, zoneID string) error
	TriggerActivationCheck(ctx context.Context, zoneID string) error
}
