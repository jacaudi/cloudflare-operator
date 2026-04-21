package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
)

const (
	testRulesetID    = "rs-1"
	testRulesetPhase = "http_request_firewall_custom"
)

// newTestRulesetClient creates a RulesetClient backed by a test HTTP server.
func newTestRulesetClient(t *testing.T, handler http.Handler) RulesetClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfClient := cfgo.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
	)
	return NewRulesetClientFromCF(cfClient)
}

func TestRulesetClient_GetPhaseEntrypoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/phases/"+testRulesetPhase+"/entrypoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":          testRulesetID,
			"name":        "Zone custom ruleset",
			"phase":       testRulesetPhase,
			"kind":        "zone",
			"version":     "1",
			"description": "Custom security rules",
			"rules": []map[string]any{
				{
					"id":           "rule-1",
					"action":       "block",
					"expression":   "(cf.client.bot)",
					"description":  "Block bots",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2026-01-01T00:00:00Z",
				},
			},
			"last_updated": "2026-01-01T00:00:00Z",
		}))
	})

	client := newTestRulesetClient(t, mux)
	rs, err := client.GetPhaseEntrypoint(context.Background(), "zone-1", testRulesetPhase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rs.ID != testRulesetID {
		t.Errorf("expected ID %s, got %s", testRulesetID, rs.ID)
	}
	if rs.Phase != testRulesetPhase {
		t.Errorf("expected phase %s, got %s", testRulesetPhase, rs.Phase)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}
	if rs.Rules[0].Action != "block" {
		t.Errorf("expected action block, got %s", rs.Rules[0].Action)
	}
}

func TestRulesetClient_GetPhaseEntrypoint_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/phases/"+testRulesetPhase+"/entrypoint", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 10007, "message": "no entrypoint exists for the given phase"}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})

	client := newTestRulesetClient(t, mux)
	_, err := client.GetPhaseEntrypoint(context.Background(), "zone-1", testRulesetPhase)
	if err == nil {
		t.Fatal("expected error for missing phase entrypoint")
	}
	if !errors.Is(err, ErrPhaseEntrypointNotFound) {
		t.Errorf("expected ErrPhaseEntrypointNotFound, got %v", err)
	}
}

func TestRulesetClient_UpsertPhaseEntrypoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/phases/"+testRulesetPhase+"/entrypoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify a couple of request fields made it through.
		if name, _ := body["name"].(string); name != "Zone custom ruleset" {
			t.Errorf("expected name='Zone custom ruleset', got %q", name)
		}
		rules, ok := body["rules"].([]any)
		if !ok {
			t.Fatal("expected rules to be an array in the request body")
		}
		if len(rules) != 2 {
			t.Errorf("expected 2 rules in request body, got %d", len(rules))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":          testRulesetID,
			"name":        "Zone custom ruleset",
			"phase":       testRulesetPhase,
			"kind":        "zone",
			"version":     "2",
			"description": "Custom security rules",
			"rules": []map[string]any{
				{
					"id":           "rule-1",
					"action":       "block",
					"expression":   "(cf.client.bot)",
					"description":  "Block bots",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2026-01-01T00:00:00Z",
				},
				{
					"id":           "rule-2",
					"action":       "block",
					"expression":   "(ip.geoip.country ne \"US\")",
					"description":  "Block non-US",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2026-01-01T00:00:00Z",
				},
			},
			"last_updated": "2026-01-01T00:00:00Z",
		}))
	})

	client := newTestRulesetClient(t, mux)
	params := RulesetParams{
		Name:        "Zone custom ruleset",
		Description: "Custom security rules",
		Phase:       testRulesetPhase,
		Rules: []RulesetRule{
			{Action: "block", Expression: "(cf.client.bot)", Description: "Block bots", Enabled: true},
			{Action: "block", Expression: "(ip.geoip.country ne \"US\")", Description: "Block non-US", Enabled: true},
		},
	}
	rs, err := client.UpsertPhaseEntrypoint(context.Background(), "zone-1", testRulesetPhase, params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rs.ID != testRulesetID {
		t.Errorf("expected ID %s, got %s", testRulesetID, rs.ID)
	}
	if len(rs.Rules) != 2 {
		t.Errorf("expected 2 rules in response, got %d", len(rs.Rules))
	}
}
