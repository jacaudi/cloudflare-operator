package cloudflare

import (
	"context"
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/bot_management"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

// zoneClient wraps the cloudflare-go v6 SDK to implement ZoneClient.
type zoneClient struct {
	cf *cfgo.Client
}

// NewZoneClientFromCF creates a ZoneClient from a cloudflare-go Client.
func NewZoneClientFromCF(cf *cfgo.Client) ZoneClient {
	return &zoneClient{cf: cf}
}

func (c *zoneClient) GetSettings(ctx context.Context, zoneID string) ([]ZoneSetting, error) {
	// The v6 SDK does not expose a "list all settings" endpoint.
	// The controller sets settings idempotently, so this returns nil.
	return nil, nil
}

func (c *zoneClient) UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error {
	_, err := c.cf.Zones.Settings.Edit(ctx, settingID, zones.SettingEditParams{
		ZoneID: cfgo.F(zoneID),
		Body: zones.SettingEditParamsBody{
			Value: cfgo.F[any](value),
		},
	})
	if err != nil {
		return fmt.Errorf("update zone setting %s: %w", settingID, err)
	}
	return nil
}

func (c *zoneClient) GetBotManagement(ctx context.Context, zoneID string) (*BotManagementConfig, error) {
	resp, err := c.cf.BotManagement.Get(ctx, bot_management.BotManagementGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get bot management: %w", err)
	}
	return &BotManagementConfig{
		EnableJS:  resp.EnableJS,
		FightMode: resp.FightMode,
	}, nil
}

func (c *zoneClient) UpdateBotManagement(ctx context.Context, zoneID string, config BotManagementConfig) error {
	_, err := c.cf.BotManagement.Update(ctx, bot_management.BotManagementUpdateParams{
		ZoneID: cfgo.F(zoneID),
		Body: bot_management.BotFightModeConfigurationParam{
			EnableJS:  cfgo.F(config.EnableJS),
			FightMode: cfgo.F(config.FightMode),
		},
	})
	if err != nil {
		return fmt.Errorf("update bot management: %w", err)
	}
	return nil
}
