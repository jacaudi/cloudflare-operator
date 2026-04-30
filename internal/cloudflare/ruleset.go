package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/rulesets"
)

// ErrPhaseEntrypointNotFound is returned by GetPhaseEntrypoint when no
// entrypoint ruleset has been created yet for the requested phase. The caller
// should treat this as "start from scratch" and proceed to UpsertPhaseEntrypoint.
var ErrPhaseEntrypointNotFound = errors.New("phase entrypoint not found")

// rulesetClient wraps the cloudflare-go v6 SDK to implement RulesetClient.
type rulesetClient struct {
	cf *cfgo.Client
}

// NewRulesetClientFromCF creates a RulesetClient from a cloudflare-go Client.
func NewRulesetClientFromCF(cf *cfgo.Client) RulesetClient {
	return &rulesetClient{cf: cf}
}

func (c *rulesetClient) GetPhaseEntrypoint(ctx context.Context, zoneID, phase string) (*Ruleset, error) {
	resp, err := c.cf.Rulesets.Phases.Get(ctx, rulesets.Phase(phase), rulesets.PhaseGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		var apiErr *cfgo.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: phase %s in zone %s", ErrPhaseEntrypointNotFound, phase, zoneID)
		}
		return nil, fmt.Errorf("get phase entrypoint %s: %w", phase, err)
	}
	return mapPhaseGetResponse(resp), nil
}

func (c *rulesetClient) UpsertPhaseEntrypoint(ctx context.Context, zoneID, phase string, params RulesetParams) (*Ruleset, error) {
	resp, err := c.cf.Rulesets.Phases.Update(ctx, rulesets.Phase(phase), rulesets.PhaseUpdateParams{
		ZoneID:      cfgo.F(zoneID),
		Name:        cfgo.F(params.Name),
		Description: cfgo.F(params.Description),
		Rules:       cfgo.F(buildPhaseUpdateRules(params.Rules)),
	})
	if err != nil {
		return nil, fmt.Errorf("upsert phase entrypoint %s: %w", phase, err)
	}
	return mapPhaseUpdateResponse(resp), nil
}

// buildPhaseUpdateRules converts internal RulesetRule slices to the SDK's
// phase-update rule union slice.
func buildPhaseUpdateRules(rules []RulesetRule) []rulesets.PhaseUpdateParamsRuleUnion {
	sdkRules := make([]rulesets.PhaseUpdateParamsRuleUnion, 0, len(rules))
	for _, r := range rules {
		rule := rulesets.PhaseUpdateParamsRule{
			Action:      cfgo.F(rulesets.PhaseUpdateParamsRulesAction(r.Action)),
			Expression:  cfgo.F(r.Expression),
			Description: cfgo.F(r.Description),
			Enabled:     cfgo.F(r.Enabled),
		}
		if r.ActionParameters != nil {
			rule.ActionParameters = cfgo.F[any](r.ActionParameters)
		}
		if r.Logging != nil && r.Logging.Enabled != nil {
			rule.Logging = cfgo.F(rulesets.LoggingParam{
				Enabled: cfgo.F(*r.Logging.Enabled),
			})
		}
		sdkRules = append(sdkRules, rule)
	}
	return sdkRules
}

// toMapStringAny converts an interface{} value to map[string]any via JSON roundtrip.
// This handles both raw map[string]any and SDK typed structs (e.g. BlockRuleActionParameters).
func toMapStringAny(v any) map[string]any {
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

// mapPhaseGetResponse converts a Cloudflare SDK PhaseGetResponse to our internal Ruleset.
func mapPhaseGetResponse(resp *rulesets.PhaseGetResponse) *Ruleset {
	rs := &Ruleset{
		ID:          resp.ID,
		Name:        resp.Name,
		Description: resp.Description,
		Phase:       string(resp.Phase),
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
		// Treat the SDK's Enabled=true as "logging configured"; Enabled=false is
		// indistinguishable from "logging not configured" on this response shape
		// (Cloudflare returns enabled=false when no logging block is present), so
		// we leave Logging nil to avoid spurious diffs against desired state where
		// the user did not set logging.
		if r.Logging.Enabled {
			t := true
			rule.Logging = &RuleLogging{Enabled: &t}
		}
		rs.Rules = append(rs.Rules, rule)
	}
	return rs
}

// mapPhaseUpdateResponse converts a Cloudflare SDK PhaseUpdateResponse to our internal Ruleset.
func mapPhaseUpdateResponse(resp *rulesets.PhaseUpdateResponse) *Ruleset {
	rs := &Ruleset{
		ID:          resp.ID,
		Name:        resp.Name,
		Description: resp.Description,
		Phase:       string(resp.Phase),
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
		// Treat the SDK's Enabled=true as "logging configured"; Enabled=false is
		// indistinguishable from "logging not configured" on this response shape
		// (Cloudflare returns enabled=false when no logging block is present), so
		// we leave Logging nil to avoid spurious diffs against desired state where
		// the user did not set logging.
		if r.Logging.Enabled {
			t := true
			rule.Logging = &RuleLogging{Enabled: &t}
		}
		rs.Rules = append(rs.Rules, rule)
	}
	return rs
}
