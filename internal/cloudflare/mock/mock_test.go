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

package mock

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

func TestMock_ZoneLifecycle(t *testing.T) {
	m := New()
	ctx := context.Background()

	z, err := m.Zone.CreateZone(ctx, "acct-1", cloudflare.ZoneParams{Name: "example.com", Type: "full"})
	require.NoError(t, err)
	require.NotEmpty(t, z.ID)
	require.Equal(t, "pending", z.Status)
	require.Len(t, z.NameServers, 2)

	got, err := m.Zone.GetZone(ctx, z.ID)
	require.NoError(t, err)
	require.Equal(t, "example.com", got.Name)

	list, err := m.Zone.ListZonesByName(ctx, "acct-1", "example.com")
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestMock_ZoneActivationCheck_Advances(t *testing.T) {
	m := New()
	ctx := context.Background()
	z, _ := m.Zone.CreateZone(ctx, "acct-1", cloudflare.ZoneParams{Name: "example.com"})
	require.NoError(t, m.Zone.TriggerActivationCheck(ctx, z.ID))
	got, _ := m.Zone.GetZone(ctx, z.ID)
	require.Equal(t, "active", got.Status)
}

func TestMock_ZoneDelete_NotFound(t *testing.T) {
	m := New()
	err := m.Zone.DeleteZone(context.Background(), "missing")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestMock_DNS_CRUD(t *testing.T) {
	m := New()
	ctx := context.Background()
	r, err := m.DNS.CreateRecord(ctx, "z1", cloudflare.DNSRecordParams{
		Name: "app.example.com", Type: "A", Content: "192.0.2.1", TTL: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, r.ID)
	list, err := m.DNS.ListRecordsByNameAndType(ctx, "z1", "app.example.com", "A")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, m.DNS.DeleteRecord(ctx, "z1", r.ID))
	list2, _ := m.DNS.ListRecordsByNameAndType(ctx, "z1", "app.example.com", "A")
	require.Len(t, list2, 0)
}

func TestMock_Ruleset_UpsertReplacesRules(t *testing.T) {
	m := New()
	ctx := context.Background()
	rs, err := m.Ruleset.UpsertPhaseEntrypoint(ctx, "z1", "http_request_firewall_custom", cloudflare.RulesetParams{
		Name: "waf", Phase: "http_request_firewall_custom",
		Rules: []cloudflare.RulesetRule{{Action: "block", Expression: "true", Enabled: true}},
	})
	require.NoError(t, err)
	require.Len(t, rs.Rules, 1)
	rs2, err := m.Ruleset.UpsertPhaseEntrypoint(ctx, "z1", "http_request_firewall_custom", cloudflare.RulesetParams{
		Name: "waf", Phase: "http_request_firewall_custom",
		Rules: []cloudflare.RulesetRule{
			{Action: "block", Expression: "true", Enabled: true},
			{Action: "log", Expression: "false", Enabled: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, rs.ID, rs2.ID, "upsert keeps same ID")
	require.Len(t, rs2.Rules, 2, "PUT-replace semantics")
}

func TestMock_ZoneConfig_BotManagement(t *testing.T) {
	m := New()
	ctx := context.Background()
	enable := true
	require.NoError(t, m.ZoneConfig.UpdateBotManagement(ctx, "z1", cloudflare.BotManagementConfig{EnableJS: &enable}))
	got, err := m.ZoneConfig.GetBotManagement(ctx, "z1")
	require.NoError(t, err)
	require.NotNil(t, got.EnableJS)
	require.True(t, *got.EnableJS)
}

func TestMock_FailureInjection(t *testing.T) {
	m := New()
	m.InjectError("Zone.CreateZone", errors.New("simulated 500"))
	_, err := m.Zone.CreateZone(context.Background(), "acct", cloudflare.ZoneParams{Name: "x.example.com"})
	require.Error(t, err)
	require.EqualError(t, err, "simulated 500")
}
