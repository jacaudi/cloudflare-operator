package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/rulesets"
)

// rulesetClient wraps the cloudflare-go v6 SDK to implement RulesetClient.
type rulesetClient struct {
	cf *cfgo.Client
}

// NewRulesetClientFromCF creates a RulesetClient from a cloudflare-go Client.
func NewRulesetClientFromCF(cf *cfgo.Client) RulesetClient {
	return &rulesetClient{cf: cf}
}

func (c *rulesetClient) GetRuleset(ctx context.Context, zoneID, rulesetID string) (*Ruleset, error) {
	resp, err := c.cf.Rulesets.Get(ctx, rulesetID, rulesets.RulesetGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get ruleset %s: %w", rulesetID, err)
	}
	return mapGetRulesetResponse(resp), nil
}

func (c *rulesetClient) ListRulesetsByPhase(ctx context.Context, zoneID, phase string) ([]Ruleset, error) {
	page, err := c.cf.Rulesets.List(ctx, rulesets.RulesetListParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("list rulesets: %w", err)
	}

	var result []Ruleset
	for _, rs := range page.Result {
		if string(rs.Phase) == phase {
			result = append(result, Ruleset{
				ID:    rs.ID,
				Name:  rs.Name,
				Phase: string(rs.Phase),
			})
		}
	}
	return result, nil
}

func (c *rulesetClient) CreateRuleset(ctx context.Context, zoneID string, params RulesetParams) (*Ruleset, error) {
	newRules := buildNewParamsRules(params.Rules)

	resp, err := c.cf.Rulesets.New(ctx, rulesets.RulesetNewParams{
		Kind:        cfgo.F(rulesets.KindCustom),
		Name:        cfgo.F(params.Name),
		Phase:       cfgo.F(rulesets.Phase(params.Phase)),
		ZoneID:      cfgo.F(zoneID),
		Description: cfgo.F(params.Description),
		Rules:       cfgo.F(newRules),
	})
	if err != nil {
		return nil, fmt.Errorf("create ruleset: %w", err)
	}
	return mapNewRulesetResponse(resp), nil
}

func (c *rulesetClient) UpdateRuleset(ctx context.Context, zoneID, rulesetID string, params RulesetParams) (*Ruleset, error) {
	updateRules := buildUpdateParamsRules(params.Rules)

	resp, err := c.cf.Rulesets.Update(ctx, rulesetID, rulesets.RulesetUpdateParams{
		ZoneID:      cfgo.F(zoneID),
		Name:        cfgo.F(params.Name),
		Description: cfgo.F(params.Description),
		Phase:       cfgo.F(rulesets.Phase(params.Phase)),
		Kind:        cfgo.F(rulesets.KindCustom),
		Rules:       cfgo.F(updateRules),
	})
	if err != nil {
		return nil, fmt.Errorf("update ruleset %s: %w", rulesetID, err)
	}
	return mapUpdateRulesetResponse(resp), nil
}

func (c *rulesetClient) DeleteRuleset(ctx context.Context, zoneID, rulesetID string) error {
	err := c.cf.Rulesets.Delete(ctx, rulesetID, rulesets.RulesetDeleteParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete ruleset %s: %w", rulesetID, err)
	}
	return nil
}

// buildNewParamsRules converts internal RulesetRule slices to SDK RulesetNewParamsRuleUnion slices.
func buildNewParamsRules(rules []RulesetRule) []rulesets.RulesetNewParamsRuleUnion {
	sdkRules := make([]rulesets.RulesetNewParamsRuleUnion, 0, len(rules))
	for _, r := range rules {
		sdkRules = append(sdkRules, rulesets.RulesetNewParamsRule{
			Action:      cfgo.F(rulesets.RulesetNewParamsRulesAction(r.Action)),
			Expression:  cfgo.F(r.Expression),
			Description: cfgo.F(r.Description),
			Enabled:     cfgo.F(r.Enabled),
		})
	}
	return sdkRules
}

// buildUpdateParamsRules converts internal RulesetRule slices to SDK RulesetUpdateParamsRuleUnion slices.
func buildUpdateParamsRules(rules []RulesetRule) []rulesets.RulesetUpdateParamsRuleUnion {
	sdkRules := make([]rulesets.RulesetUpdateParamsRuleUnion, 0, len(rules))
	for _, r := range rules {
		sdkRules = append(sdkRules, rulesets.RulesetUpdateParamsRule{
			Action:      cfgo.F(rulesets.RulesetUpdateParamsRulesAction(r.Action)),
			Expression:  cfgo.F(r.Expression),
			Description: cfgo.F(r.Description),
			Enabled:     cfgo.F(r.Enabled),
		})
	}
	return sdkRules
}

// toMapStringAny converts an interface{} value to map[string]any via JSON roundtrip.
// This handles both raw map[string]any and SDK typed structs (e.g. BlockRuleActionParameters).
func toMapStringAny(v interface{}) map[string]any {
	if v == nil {
		return nil
	}
	// Fast path: already a map[string]any.
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// Slow path: marshal and unmarshal through JSON.
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// mapGetRulesetResponse converts a Cloudflare SDK RulesetGetResponse to our internal Ruleset.
func mapGetRulesetResponse(resp *rulesets.RulesetGetResponse) *Ruleset {
	rs := &Ruleset{
		ID:    resp.ID,
		Name:  resp.Name,
		Phase: string(resp.Phase),
	}
	for _, r := range resp.Rules {
		rule := RulesetRule{
			ID:          r.ID,
			Action:      string(r.Action),
			Expression:  r.Expression,
			Description: r.Description,
			Enabled:     r.Enabled,
		}
		rule.ActionParameters = toMapStringAny(r.ActionParameters)
		rs.Rules = append(rs.Rules, rule)
	}
	return rs
}

// mapNewRulesetResponse converts a Cloudflare SDK RulesetNewResponse to our internal Ruleset.
func mapNewRulesetResponse(resp *rulesets.RulesetNewResponse) *Ruleset {
	rs := &Ruleset{
		ID:    resp.ID,
		Name:  resp.Name,
		Phase: string(resp.Phase),
	}
	for _, r := range resp.Rules {
		rule := RulesetRule{
			ID:          r.ID,
			Action:      string(r.Action),
			Expression:  r.Expression,
			Description: r.Description,
			Enabled:     r.Enabled,
		}
		rule.ActionParameters = toMapStringAny(r.ActionParameters)
		rs.Rules = append(rs.Rules, rule)
	}
	return rs
}

// mapUpdateRulesetResponse converts a Cloudflare SDK RulesetUpdateResponse to our internal Ruleset.
func mapUpdateRulesetResponse(resp *rulesets.RulesetUpdateResponse) *Ruleset {
	rs := &Ruleset{
		ID:    resp.ID,
		Name:  resp.Name,
		Phase: string(resp.Phase),
	}
	for _, r := range resp.Rules {
		rule := RulesetRule{
			ID:          r.ID,
			Action:      string(r.Action),
			Expression:  r.Expression,
			Description: r.Description,
			Enabled:     r.Enabled,
		}
		rule.ActionParameters = toMapStringAny(r.ActionParameters)
		rs.Rules = append(rs.Rules, rule)
	}
	return rs
}
