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

// Package cloudflare wraps the cloudflare-go SDK with the operator's
// credential resolution, error classification, and retry semantics.
//
// Per Foundation §6.1.1, interfaces.go is append-only across specs:
// spec 2 ships the four zone-bundle interfaces below; spec 3 appends
// TunnelClient.
package cloudflare

import (
	"context"
	"time"
)

// --- DNS ---

// DNSRecord is the operator-side representation of a Cloudflare DNS record.
type DNSRecord struct {
	ID       string
	Name     string
	Type     string
	Content  string
	Proxied  bool
	TTL      int
	Priority *int
	Data     map[string]any
}

// DNSRecordParams are the inputs to Create / Update.
type DNSRecordParams struct {
	Name     string
	Type     string
	Content  string
	Proxied  *bool
	TTL      int
	Priority *int
	Data     map[string]any
}

// DNSClient manages Cloudflare DNS records under /zones/{zone_id}/dns_records.
type DNSClient interface {
	GetRecord(ctx context.Context, zoneID, recordID string) (*DNSRecord, error)
	ListRecordsByNameAndType(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, zoneID string, params DNSRecordParams) (*DNSRecord, error)
	UpdateRecord(ctx context.Context, zoneID, recordID string, params DNSRecordParams) (*DNSRecord, error)
	DeleteRecord(ctx context.Context, zoneID, recordID string) error
}

// --- Zone (lifecycle) ---

// Zone is the operator-side representation of a Cloudflare zone.
type Zone struct {
	ID                  string
	Name                string
	Status              string // initializing | pending | active | moved
	Type                string // full | partial | secondary
	Paused              bool
	NameServers         []string
	OriginalNameServers []string
	OriginalRegistrar   string
	ActivatedOn         *time.Time
}

// ZoneParams are the inputs to Create.
type ZoneParams struct {
	Name string
	Type string
}

// ZoneEditParams are the inputs to Edit.
type ZoneEditParams struct {
	Paused *bool
}

// ZoneClient manages zone lifecycle under /zones.
type ZoneClient interface {
	CreateZone(ctx context.Context, accountID string, params ZoneParams) (*Zone, error)
	GetZone(ctx context.Context, zoneID string) (*Zone, error)
	ListZonesByName(ctx context.Context, accountID, name string) ([]Zone, error)
	EditZone(ctx context.Context, zoneID string, params ZoneEditParams) (*Zone, error)
	DeleteZone(ctx context.Context, zoneID string) error
	TriggerActivationCheck(ctx context.Context, zoneID string) error
}

// --- ZoneConfig (settings + bot management) ---

// ZoneSetting is a key-value pair for a zone setting.
type ZoneSetting struct {
	ID    string
	Value any
}

// BotManagementConfig represents bot management settings.
// Pointer fields distinguish "unset" from "set to false".
type BotManagementConfig struct {
	EnableJS  *bool
	FightMode *bool
}

// ZoneConfigClient manages /zones/{zone_id}/settings and /zones/{zone_id}/bot_management.
type ZoneConfigClient interface {
	UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error
	GetBotManagement(ctx context.Context, zoneID string) (*BotManagementConfig, error)
	UpdateBotManagement(ctx context.Context, zoneID string, config BotManagementConfig) error
}

// --- Ruleset ---

// Ruleset is the operator-side representation of a zone ruleset.
type Ruleset struct {
	ID          string
	Name        string
	Description string
	Phase       string
	Rules       []RulesetRule
}

// RuleLogging is the per-rule logging override.
type RuleLogging struct {
	Enabled *bool
}

// RulesetRule is one rule inside a ruleset.
type RulesetRule struct {
	ID               string
	Action           string
	Expression       string
	Description      string
	Enabled          bool
	ActionParameters map[string]any
	Logging          *RuleLogging
}

// RulesetParams are the inputs to UpsertPhaseEntrypoint.
type RulesetParams struct {
	Name        string
	Description string
	Phase       string
	Rules       []RulesetRule
}

// RulesetClient manages the zone's phase-entrypoint rulesets via the
// PUT /zones/{zone_id}/rulesets/phases/{phase}/entrypoint write path.
type RulesetClient interface {
	GetPhaseEntrypoint(ctx context.Context, zoneID, phase string) (*Ruleset, error)
	UpsertPhaseEntrypoint(ctx context.Context, zoneID, phase string, params RulesetParams) (*Ruleset, error)
}
