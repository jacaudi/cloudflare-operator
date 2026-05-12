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

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/bot_management"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

// ErrPlanTierInsufficient is returned by ZoneConfigClient methods when the
// Cloudflare API responds with 403 Forbidden to a zone-setting or
// bot-management update. For these specific endpoints, a 403 in practice
// indicates the zone's plan tier does not include the feature being set
// (e.g. bot management on a Free zone). Controllers use this sentinel to
// surface a PlanTierInsufficient condition reason rather than a generic
// transient error. Match with errors.Is.
var ErrPlanTierInsufficient = errors.New("cloudflare plan tier does not support this setting")

// zoneConfigClient wraps the cloudflare-go v6 SDK to implement ZoneConfigClient.
// It covers zone-level settings (PUT /zones/{id}/settings/{setting_id}) and
// the bot-management resource (GET/PUT /zones/{id}/bot_management).
type zoneConfigClient struct {
	cf *cfgo.Client
}

// NewZoneConfigClientFromCF creates a ZoneConfigClient from a cloudflare-go Client.
func NewZoneConfigClientFromCF(cf *cfgo.Client) ZoneConfigClient {
	return &zoneConfigClient{cf: cf}
}

// UpdateSetting PUTs a single zone setting via /zones/{id}/settings/{setting_id}.
// A 403 from this endpoint is classified as ErrPlanTierInsufficient because
// plan-tier gating is the dominant cause of 403s on settings writes; other
// 403 causes (token scope, etc.) are surfaced via the wrapped error and can
// still be inspected with errors.As on *cfgo.Error.
func (c *zoneConfigClient) UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error {
	_, err := c.cf.Zones.Settings.Edit(ctx, settingID, zones.SettingEditParams{
		ZoneID: cfgo.F(zoneID),
		Body: zones.SettingEditParamsBody{
			Value: cfgo.F[any](value),
		},
	})
	if err != nil {
		return fmt.Errorf("update zone setting %s: %w", settingID, classifyZoneConfigAPIErr(err))
	}
	return nil
}

// GetBotManagement GETs /zones/{id}/bot_management and projects the response
// onto the operator-side BotManagementConfig (EnableJS, FightMode pointers).
func (c *zoneConfigClient) GetBotManagement(ctx context.Context, zoneID string) (*BotManagementConfig, error) {
	resp, err := c.cf.BotManagement.Get(ctx, bot_management.BotManagementGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get bot management: %w", err)
	}
	enableJS := resp.EnableJS
	fightMode := resp.FightMode
	return &BotManagementConfig{
		EnableJS:  &enableJS,
		FightMode: &fightMode,
	}, nil
}

// UpdateBotManagement PUTs /zones/{id}/bot_management with the provided pointers.
// A 403 from this endpoint is classified as ErrPlanTierInsufficient: bot
// management is a paid feature, and 403 on this endpoint in practice means
// the zone's plan does not include it. Callers can match with errors.Is and
// still inspect the wrapped *cfgo.Error for the underlying detail.
func (c *zoneConfigClient) UpdateBotManagement(ctx context.Context, zoneID string, config BotManagementConfig) error {
	body := bot_management.BotFightModeConfigurationParam{}
	if config.EnableJS != nil {
		body.EnableJS = cfgo.F(*config.EnableJS)
	}
	if config.FightMode != nil {
		body.FightMode = cfgo.F(*config.FightMode)
	}
	_, err := c.cf.BotManagement.Update(ctx, bot_management.BotManagementUpdateParams{
		ZoneID: cfgo.F(zoneID),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("update bot management: %w", classifyZoneConfigAPIErr(err))
	}
	return nil
}

// classifyZoneConfigAPIErr wraps a Cloudflare API error from a setting-update
// or bot-management call with ErrPlanTierInsufficient when the status is 403.
// This is intentionally scoped to endpoints where plan-tier gating is the
// dominant cause of 403; do not reuse it on endpoints with other 403 modes
// (e.g. token-scope errors on zone reads).
func classifyZoneConfigAPIErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *cfgo.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrPlanTierInsufficient, err)
		}
	}
	return err
}
