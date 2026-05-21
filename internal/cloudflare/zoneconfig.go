/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
// 403 responses are classified through classifyZoneConfigAPIErr so callers
// receive ErrPlanTierInsufficient symmetrically with UpdateBotManagement.
func (c *zoneConfigClient) GetBotManagement(ctx context.Context, zoneID string) (*BotManagementConfig, error) {
	resp, err := c.cf.BotManagement.Get(ctx, bot_management.BotManagementGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get bot management: %w", classifyZoneConfigAPIErr(err))
	}
	enableJS := resp.EnableJS
	fightMode := resp.FightMode
	return &BotManagementConfig{
		EnableJS:  &enableJS,
		FightMode: &fightMode,
	}, nil
}

// UpdateBotManagement PUTs the provided config to Cloudflare's
// /zones/{id}/bot_management endpoint.
//
// Cloudflare's PUT semantics treat absent JSON fields as "leave unchanged";
// cfgo's param.Field zero-omit means setting BotManagementConfig fields
// (EnableJS, FightMode) to nil pointers will omit them from the request
// body, preserving the existing zone-side values. To explicitly clear a
// previously-set field, pass a pointer to its zero value.
//
// A 403 from this endpoint is classified as ErrPlanTierInsufficient (via
// classifyZoneConfigAPIErr's message-keyword match): bot management is a
// paid feature, and a plan-tier 403 is the dominant cause here. Callers
// can match with errors.Is and still inspect the wrapped *cfgo.Error for
// the underlying detail.
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

// classifyZoneConfigAPIErr inspects a Cloudflare API error from a
// setting-update or bot-management call. A 403 is wrapped with
// ErrPlanTierInsufficient *only* when the response message looks like a
// plan-tier rejection (vs. token-scope, IP-restriction, or account-
// suspension 403s, which are also surfaced as 403 by the API).
//
// False negatives (a genuine plan-tier 403 returned with an unrecognized
// message) fall through to the raw error — the operator's status will
// still surface the underlying Cloudflare message, just not the typed
// sentinel. This is preferred over false positives, which would
// mis-label e.g. a token-scope problem as a plan limitation and steer
// users toward the wrong fix.
func classifyZoneConfigAPIErr(err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := errors.AsType[*cfgo.Error](err); ok && apiErr.StatusCode == http.StatusForbidden {
		if isPlanTier403(apiErr) {
			return fmt.Errorf("%w: %w", ErrPlanTierInsufficient, err)
		}
	}
	return err
}

// isPlanTier403 decides whether a 403 from a zone-config endpoint is a
// plan-tier rejection. We match on the Cloudflare error message keywords
// because the numeric codes vary across endpoints and aren't reliably
// distinct between plan-tier and other 403 causes.
func isPlanTier403(apiErr *cfgo.Error) bool {
	for _, e := range apiErr.Errors {
		msg := strings.ToLower(e.Message)
		if strings.Contains(msg, "plan") ||
			strings.Contains(msg, "subscription") ||
			strings.Contains(msg, "upgrade") {
			return true
		}
	}
	return false
}
