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

// Package mock provides an in-memory Cloudflare API for envtest suites.
// One mock satisfies all four zone-bundle interfaces from internal/cloudflare.
// Spec 3 will extend the same package with TunnelClient mock state.
package mock

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrNotFound is returned by getters / deleters when the requested object
// is absent. Reconcilers should treat this like a Cloudflare 404 — use
// errors.Is to match.
var ErrNotFound = errors.New("mock cloudflare: not found")

// Mock is the central state holder. Construct via New; pass the typed
// sub-fields (Mock.Zone, Mock.DNS, etc.) to reconcilers under test.
type Mock struct {
	Zone       *zoneMock
	DNS        *dnsMock
	Ruleset    *rulesetMock
	ZoneConfig *zoneConfigMock

	mu        sync.Mutex
	injectors map[string]error
}

// New returns an initialized Mock.
func New() *Mock {
	m := &Mock{injectors: map[string]error{}}
	m.Zone = &zoneMock{parent: m, zones: map[string]*cloudflare.Zone{}}
	m.DNS = &dnsMock{parent: m, records: map[string]map[string]*cloudflare.DNSRecord{}}
	m.Ruleset = &rulesetMock{parent: m, entries: map[string]map[string]*cloudflare.Ruleset{}}
	m.ZoneConfig = &zoneConfigMock{parent: m, settings: map[string]map[string]any{}, bm: map[string]cloudflare.BotManagementConfig{}}
	return m
}

// InjectError schedules the next call to the named method to return err.
// Method names use the form "Sub.Method" (e.g. "Zone.CreateZone"). The
// injection consumes on first match.
func (m *Mock) InjectError(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectors[method] = err
}

func (m *Mock) take(method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectors[method]; ok {
		delete(m.injectors, method)
		return err
	}
	return nil
}

// --- zone ---

type zoneMock struct {
	parent  *Mock
	mu      sync.Mutex
	seq     atomic.Uint64
	zones   map[string]*cloudflare.Zone
	nameIdx map[string]string // accountID|name -> zoneID
}

func (z *zoneMock) CreateZone(ctx context.Context, accountID string, params cloudflare.ZoneParams) (*cloudflare.Zone, error) {
	if err := z.parent.take("Zone.CreateZone"); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.nameIdx == nil {
		z.nameIdx = map[string]string{}
	}
	key := accountID + "|" + params.Name
	if existing, ok := z.nameIdx[key]; ok {
		return z.zones[existing], nil
	}
	id := "z" + strconv.FormatUint(z.seq.Add(1), 10)
	now := time.Now()
	zoneType := params.Type
	if zoneType == "" {
		zoneType = "full"
	}
	z.zones[id] = &cloudflare.Zone{
		ID: id, Name: params.Name, Status: "pending", Type: zoneType,
		NameServers:         []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
		OriginalNameServers: []string{"ns1.example.com", "ns2.example.com"},
		ActivatedOn:         &now,
	}
	z.nameIdx[key] = id
	return z.zones[id], nil
}

func (z *zoneMock) GetZone(ctx context.Context, zoneID string) (*cloudflare.Zone, error) {
	if err := z.parent.take("Zone.GetZone"); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	got, ok := z.zones[zoneID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrZoneNotFound, zoneID)
	}
	return got, nil
}

func (z *zoneMock) ListZonesByName(ctx context.Context, accountID, name string) ([]cloudflare.Zone, error) {
	if err := z.parent.take("Zone.ListZonesByName"); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	out := []cloudflare.Zone{}
	for _, zz := range z.zones {
		if zz.Name == name {
			out = append(out, *zz)
		}
	}
	return out, nil
}

func (z *zoneMock) EditZone(ctx context.Context, zoneID string, params cloudflare.ZoneEditParams) (*cloudflare.Zone, error) {
	if err := z.parent.take("Zone.EditZone"); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	got, ok := z.zones[zoneID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrZoneNotFound, zoneID)
	}
	if params.Paused != nil {
		got.Paused = *params.Paused
	}
	return got, nil
}

func (z *zoneMock) DeleteZone(ctx context.Context, zoneID string) error {
	if err := z.parent.take("Zone.DeleteZone"); err != nil {
		return err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if _, ok := z.zones[zoneID]; !ok {
		return fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrZoneNotFound, zoneID)
	}
	name := z.zones[zoneID].Name
	delete(z.zones, zoneID)
	// best-effort name-index cleanup
	for k, v := range z.nameIdx {
		if v == zoneID {
			delete(z.nameIdx, k)
		}
	}
	_ = name
	return nil
}

func (z *zoneMock) TriggerActivationCheck(ctx context.Context, zoneID string) error {
	if err := z.parent.take("Zone.TriggerActivationCheck"); err != nil {
		return err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	got, ok := z.zones[zoneID]
	if !ok {
		return fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrZoneNotFound, zoneID)
	}
	got.Status = "active"
	return nil
}

// --- dns ---

type dnsMock struct {
	parent  *Mock
	mu      sync.Mutex
	seq     atomic.Uint64
	records map[string]map[string]*cloudflare.DNSRecord // zoneID -> recordID -> rec
}

func (d *dnsMock) GetRecord(ctx context.Context, zoneID, recordID string) (*cloudflare.DNSRecord, error) {
	if err := d.parent.take("DNS.GetRecord"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	z, ok := d.records[zoneID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrRecordNotFound, zoneID)
	}
	r, ok := z[recordID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: record %s", ErrNotFound, cloudflare.ErrRecordNotFound, recordID)
	}
	return r, nil
}

func (d *dnsMock) ListRecordsByNameAndType(ctx context.Context, zoneID, name, recordType string) ([]cloudflare.DNSRecord, error) {
	if err := d.parent.take("DNS.ListRecordsByNameAndType"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []cloudflare.DNSRecord{}
	for _, r := range d.records[zoneID] {
		if r.Name == name && r.Type == recordType {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (d *dnsMock) CreateRecord(ctx context.Context, zoneID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	if err := d.parent.take("DNS.CreateRecord"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.records[zoneID] == nil {
		d.records[zoneID] = map[string]*cloudflare.DNSRecord{}
	}
	id := "r" + strconv.FormatUint(d.seq.Add(1), 10)
	proxied := false
	if params.Proxied != nil {
		proxied = *params.Proxied
	}
	r := &cloudflare.DNSRecord{
		ID: id, Name: params.Name, Type: params.Type, Content: params.Content,
		Proxied: proxied, TTL: params.TTL, Priority: params.Priority, Data: params.Data,
	}
	d.records[zoneID][id] = r
	return r, nil
}

func (d *dnsMock) UpdateRecord(ctx context.Context, zoneID, recordID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	if err := d.parent.take("DNS.UpdateRecord"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	z, ok := d.records[zoneID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrRecordNotFound, zoneID)
	}
	r, ok := z[recordID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: record %s", ErrNotFound, cloudflare.ErrRecordNotFound, recordID)
	}
	r.Name = params.Name
	r.Type = params.Type
	r.Content = params.Content
	r.TTL = params.TTL
	r.Priority = params.Priority
	r.Data = params.Data
	if params.Proxied != nil {
		r.Proxied = *params.Proxied
	}
	return r, nil
}

func (d *dnsMock) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	if err := d.parent.take("DNS.DeleteRecord"); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	z, ok := d.records[zoneID]
	if !ok {
		return fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrRecordNotFound, zoneID)
	}
	if _, ok := z[recordID]; !ok {
		return fmt.Errorf("%w: %w: record %s", ErrNotFound, cloudflare.ErrRecordNotFound, recordID)
	}
	delete(z, recordID)
	return nil
}

// --- ruleset ---

type rulesetMock struct {
	parent  *Mock
	mu      sync.Mutex
	seq     atomic.Uint64
	entries map[string]map[string]*cloudflare.Ruleset // zoneID -> phase -> ruleset
}

func (r *rulesetMock) GetPhaseEntrypoint(ctx context.Context, zoneID, phase string) (*cloudflare.Ruleset, error) {
	if err := r.parent.take("Ruleset.GetPhaseEntrypoint"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	z, ok := r.entries[zoneID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: zone %s", ErrNotFound, cloudflare.ErrPhaseEntrypointNotFound, zoneID)
	}
	rs, ok := z[phase]
	if !ok {
		return nil, fmt.Errorf("%w: %w: phase %s", ErrNotFound, cloudflare.ErrPhaseEntrypointNotFound, phase)
	}
	return rs, nil
}

func (r *rulesetMock) UpsertPhaseEntrypoint(ctx context.Context, zoneID, phase string, params cloudflare.RulesetParams) (*cloudflare.Ruleset, error) {
	if err := r.parent.take("Ruleset.UpsertPhaseEntrypoint"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[zoneID] == nil {
		r.entries[zoneID] = map[string]*cloudflare.Ruleset{}
	}
	existing, ok := r.entries[zoneID][phase]
	if !ok {
		id := "rs" + strconv.FormatUint(r.seq.Add(1), 10)
		existing = &cloudflare.Ruleset{ID: id, Phase: phase}
		r.entries[zoneID][phase] = existing
	}
	existing.Name = params.Name
	existing.Description = params.Description
	existing.Rules = append([]cloudflare.RulesetRule{}, params.Rules...)
	return existing, nil
}

// --- zoneconfig ---

type zoneConfigMock struct {
	parent   *Mock
	mu       sync.Mutex
	settings map[string]map[string]any // zoneID -> settingID -> value
	bm       map[string]cloudflare.BotManagementConfig
}

func (z *zoneConfigMock) UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error {
	if err := z.parent.take("ZoneConfig.UpdateSetting"); err != nil {
		return err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.settings[zoneID] == nil {
		z.settings[zoneID] = map[string]any{}
	}
	z.settings[zoneID][settingID] = value
	return nil
}

func (z *zoneConfigMock) GetBotManagement(ctx context.Context, zoneID string) (*cloudflare.BotManagementConfig, error) {
	if err := z.parent.take("ZoneConfig.GetBotManagement"); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	got, ok := z.bm[zoneID]
	if !ok {
		return &cloudflare.BotManagementConfig{}, nil
	}
	return &got, nil
}

func (z *zoneConfigMock) UpdateBotManagement(ctx context.Context, zoneID string, config cloudflare.BotManagementConfig) error {
	if err := z.parent.take("ZoneConfig.UpdateBotManagement"); err != nil {
		return err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	z.bm[zoneID] = config
	return nil
}

// Compile-time interface assertions.
var (
	_ cloudflare.ZoneClient       = (*zoneMock)(nil)
	_ cloudflare.DNSClient        = (*dnsMock)(nil)
	_ cloudflare.RulesetClient    = (*rulesetMock)(nil)
	_ cloudflare.ZoneConfigClient = (*zoneConfigMock)(nil)
)

// GetSetting is a test-only accessor (not part of the interface) used by
// envtest assertions to verify reconcilers wrote what they intended.
func (z *zoneConfigMock) GetSetting(zoneID, settingID string) (any, bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	v, ok := z.settings[zoneID][settingID]
	return v, ok
}
