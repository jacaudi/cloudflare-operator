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
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
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

func (c *zoneClient) CreateZone(ctx context.Context, accountID string, params ZoneParams) (*Zone, error) {
	zoneType := zones.TypeFull
	if params.Type != "" {
		zoneType = zones.Type(params.Type)
	}

	resp, err := c.cf.Zones.New(ctx, zones.ZoneNewParams{
		Account: cfgo.F(zones.ZoneNewParamsAccount{
			ID: cfgo.F(accountID),
		}),
		Name: cfgo.F(params.Name),
		Type: cfgo.F(zoneType),
	})
	if err != nil {
		return nil, fmt.Errorf("create zone %s: %w", params.Name, err)
	}
	return mapZoneResponse(resp), nil
}

func (c *zoneClient) GetZone(ctx context.Context, zoneID string) (*Zone, error) {
	resp, err := c.cf.Zones.Get(ctx, zones.ZoneGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get zone %s: %w", zoneID, classifyZoneAPIErr(err))
	}
	return mapZoneResponse(resp), nil
}

func (c *zoneClient) ListZonesByName(ctx context.Context, accountID, name string) ([]Zone, error) {
	page, err := c.cf.Zones.List(ctx, zones.ZoneListParams{
		Account: cfgo.F(zones.ZoneListParamsAccount{
			ID: cfgo.F(accountID),
		}),
		Name: cfgo.F(name),
	})
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}

	var result []Zone
	for _, z := range page.Result {
		result = append(result, *mapZoneResponse(&z))
	}
	return result, nil
}

func (c *zoneClient) EditZone(ctx context.Context, zoneID string, params ZoneEditParams) (*Zone, error) {
	editParams := zones.ZoneEditParams{
		ZoneID: cfgo.F(zoneID),
	}
	if params.Paused != nil {
		editParams.Paused = cfgo.F(*params.Paused)
	}

	resp, err := c.cf.Zones.Edit(ctx, editParams)
	if err != nil {
		return nil, fmt.Errorf("edit zone %s: %w", zoneID, err)
	}
	return mapZoneResponse(resp), nil
}

func (c *zoneClient) DeleteZone(ctx context.Context, zoneID string) error {
	_, err := c.cf.Zones.Delete(ctx, zones.ZoneDeleteParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete zone %s: %w", zoneID, classifyZoneAPIErr(err))
	}
	return nil
}

func (c *zoneClient) TriggerActivationCheck(ctx context.Context, zoneID string) error {
	_, err := c.cf.Zones.ActivationCheck.Trigger(ctx, zones.ActivationCheckTriggerParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("trigger activation check for zone %s: %w", zoneID, err)
	}
	return nil
}

// mapZoneResponse converts a cloudflare-go zones.Zone to our internal Zone type.
func mapZoneResponse(z *zones.Zone) *Zone {
	zone := &Zone{
		ID:                  z.ID,
		Name:                z.Name,
		Status:              string(z.Status),
		Type:                string(z.Type),
		Paused:              z.Paused,
		NameServers:         z.NameServers,
		OriginalNameServers: z.OriginalNameServers,
		OriginalRegistrar:   z.OriginalRegistrar,
	}
	if !z.ActivatedOn.IsZero() {
		t := z.ActivatedOn
		zone.ActivatedOn = &t
	}
	return zone
}
