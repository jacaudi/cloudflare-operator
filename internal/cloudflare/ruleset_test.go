package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
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

func TestRulesetClient_GetRuleset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/rs-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":          "rs-1",
			"name":        "My Custom Ruleset",
			"phase":       "http_request_firewall_custom",
			"kind":        "custom",
			"version":     "1",
			"description": "Block bad bots",
			"rules": []map[string]any{
				{
					"id":          "rule-1",
					"action":      "block",
					"expression":  "(cf.bot_management.score lt 30)",
					"description": "Block low-score bots",
					"enabled":     true,
					"version":     "1",
					"action_parameters": map[string]any{
						"response": map[string]any{
							"status_code":  403,
							"content_type": "text/plain",
						},
					},
					"last_updated": "2025-01-01T00:00:00Z",
				},
				{
					"id":           "rule-2",
					"action":       "log",
					"expression":   "(cf.bot_management.score lt 50)",
					"description":  "Log medium-score bots",
					"enabled":      false,
					"version":      "1",
					"last_updated": "2025-01-01T00:00:00Z",
				},
			},
			"last_updated": "2025-01-01T00:00:00Z",
		}))
	})

	client := newTestRulesetClient(t, mux)
	rs, err := client.GetRuleset(context.Background(), "zone-1", "rs-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rs.ID != "rs-1" {
		t.Errorf("expected ID rs-1, got %s", rs.ID)
	}
	if rs.Name != "My Custom Ruleset" {
		t.Errorf("expected name 'My Custom Ruleset', got %s", rs.Name)
	}
	if rs.Phase != "http_request_firewall_custom" {
		t.Errorf("expected phase http_request_firewall_custom, got %s", rs.Phase)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rs.Rules))
	}

	// Check first rule
	if rs.Rules[0].ID != "rule-1" {
		t.Errorf("expected rule ID rule-1, got %s", rs.Rules[0].ID)
	}
	if rs.Rules[0].Action != "block" {
		t.Errorf("expected action block, got %s", rs.Rules[0].Action)
	}
	if rs.Rules[0].Expression != "(cf.bot_management.score lt 30)" {
		t.Errorf("expected expression '(cf.bot_management.score lt 30)', got %s", rs.Rules[0].Expression)
	}
	if rs.Rules[0].Description != "Block low-score bots" {
		t.Errorf("expected description 'Block low-score bots', got %s", rs.Rules[0].Description)
	}
	if !rs.Rules[0].Enabled {
		t.Error("expected rule-1 enabled true")
	}
	if rs.Rules[0].ActionParameters == nil {
		t.Error("expected action_parameters to be non-nil")
	}

	// Check second rule
	if rs.Rules[1].ID != "rule-2" {
		t.Errorf("expected rule ID rule-2, got %s", rs.Rules[1].ID)
	}
	if rs.Rules[1].Action != "log" {
		t.Errorf("expected action log, got %s", rs.Rules[1].Action)
	}
	if rs.Rules[1].Enabled {
		t.Error("expected rule-2 enabled false")
	}
}

func TestRulesetClient_ListRulesetsByPhase(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		// List endpoint returns lightweight results (no rules)
		w.Write(cfAPIListResponse(t, []map[string]any{
			{
				"id":           "rs-1",
				"name":         "Custom Firewall",
				"phase":        "http_request_firewall_custom",
				"kind":         "custom",
				"version":      "1",
				"last_updated": "2025-01-01T00:00:00Z",
			},
			{
				"id":           "rs-2",
				"name":         "Rate Limiting",
				"phase":        "http_ratelimit",
				"kind":         "custom",
				"version":      "1",
				"last_updated": "2025-01-01T00:00:00Z",
			},
			{
				"id":           "rs-3",
				"name":         "Another Firewall",
				"phase":        "http_request_firewall_custom",
				"kind":         "custom",
				"version":      "1",
				"last_updated": "2025-01-01T00:00:00Z",
			},
		}))
	})

	client := newTestRulesetClient(t, mux)
	rulesets, err := client.ListRulesetsByPhase(context.Background(), "zone-1", "http_request_firewall_custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rulesets) != 2 {
		t.Fatalf("expected 2 rulesets for phase http_request_firewall_custom, got %d", len(rulesets))
	}

	if rulesets[0].ID != "rs-1" {
		t.Errorf("expected first ruleset ID rs-1, got %s", rulesets[0].ID)
	}
	if rulesets[0].Name != "Custom Firewall" {
		t.Errorf("expected first ruleset name 'Custom Firewall', got %s", rulesets[0].Name)
	}
	if rulesets[1].ID != "rs-3" {
		t.Errorf("expected second ruleset ID rs-3, got %s", rulesets[1].ID)
	}
}

func TestRulesetClient_ListRulesetsByPhase_NoMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIListResponse(t, []map[string]any{
			{
				"id":           "rs-1",
				"name":         "Custom Firewall",
				"phase":        "http_request_firewall_custom",
				"kind":         "custom",
				"version":      "1",
				"last_updated": "2025-01-01T00:00:00Z",
			},
		}))
	})

	client := newTestRulesetClient(t, mux)
	rulesets, err := client.ListRulesetsByPhase(context.Background(), "zone-1", "http_ratelimit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rulesets) != 0 {
		t.Errorf("expected 0 rulesets, got %d", len(rulesets))
	}
}

func TestRulesetClient_CreateRuleset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["name"] != "Block Bots" {
			t.Errorf("expected name 'Block Bots', got %v", body["name"])
		}
		if body["phase"] != "http_request_firewall_custom" {
			t.Errorf("expected phase http_request_firewall_custom, got %v", body["phase"])
		}
		if body["description"] != "Custom WAF rules" {
			t.Errorf("expected description 'Custom WAF rules', got %v", body["description"])
		}

		rules, ok := body["rules"].([]any)
		if !ok || len(rules) != 1 {
			t.Fatalf("expected 1 rule in request, got %v", body["rules"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":          "rs-new",
			"name":        "Block Bots",
			"phase":       "http_request_firewall_custom",
			"kind":        "custom",
			"version":     "1",
			"description": "Custom WAF rules",
			"rules": []map[string]any{
				{
					"id":           "rule-new",
					"action":       "block",
					"expression":   "(cf.bot_management.score lt 30)",
					"description":  "Block low-score bots",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2025-01-01T00:00:00Z",
				},
			},
			"last_updated": "2025-01-01T00:00:00Z",
		}))
	})

	client := newTestRulesetClient(t, mux)
	rs, err := client.CreateRuleset(context.Background(), "zone-1", RulesetParams{
		Name:        "Block Bots",
		Description: "Custom WAF rules",
		Phase:       "http_request_firewall_custom",
		Rules: []RulesetRule{
			{
				Action:      "block",
				Expression:  "(cf.bot_management.score lt 30)",
				Description: "Block low-score bots",
				Enabled:     true,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rs.ID != "rs-new" {
		t.Errorf("expected ID rs-new, got %s", rs.ID)
	}
	if rs.Name != "Block Bots" {
		t.Errorf("expected name 'Block Bots', got %s", rs.Name)
	}
	if rs.Phase != "http_request_firewall_custom" {
		t.Errorf("expected phase http_request_firewall_custom, got %s", rs.Phase)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.Rules))
	}
	if rs.Rules[0].ID != "rule-new" {
		t.Errorf("expected rule ID rule-new, got %s", rs.Rules[0].ID)
	}
	if rs.Rules[0].Action != "block" {
		t.Errorf("expected action block, got %s", rs.Rules[0].Action)
	}
}

func TestRulesetClient_UpdateRuleset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/rs-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["name"] != "Updated Ruleset" {
			t.Errorf("expected name 'Updated Ruleset', got %v", body["name"])
		}

		rules, ok := body["rules"].([]any)
		if !ok || len(rules) != 2 {
			t.Fatalf("expected 2 rules in request, got %v", body["rules"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":          "rs-1",
			"name":        "Updated Ruleset",
			"phase":       "http_request_firewall_custom",
			"kind":        "custom",
			"version":     "2",
			"description": "Updated rules",
			"rules": []map[string]any{
				{
					"id":           "rule-1",
					"action":       "block",
					"expression":   "(cf.bot_management.score lt 20)",
					"description":  "Block very low bots",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2025-01-01T00:00:00Z",
				},
				{
					"id":           "rule-2",
					"action":       "log",
					"expression":   "(cf.bot_management.score lt 50)",
					"description":  "Log medium bots",
					"enabled":      true,
					"version":      "1",
					"last_updated": "2025-01-01T00:00:00Z",
				},
			},
			"last_updated": "2025-01-02T00:00:00Z",
		}))
	})

	client := newTestRulesetClient(t, mux)
	rs, err := client.UpdateRuleset(context.Background(), "zone-1", "rs-1", RulesetParams{
		Name:        "Updated Ruleset",
		Description: "Updated rules",
		Phase:       "http_request_firewall_custom",
		Rules: []RulesetRule{
			{
				Action:      "block",
				Expression:  "(cf.bot_management.score lt 20)",
				Description: "Block very low bots",
				Enabled:     true,
			},
			{
				Action:      "log",
				Expression:  "(cf.bot_management.score lt 50)",
				Description: "Log medium bots",
				Enabled:     true,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rs.ID != "rs-1" {
		t.Errorf("expected ID rs-1, got %s", rs.ID)
	}
	if rs.Name != "Updated Ruleset" {
		t.Errorf("expected name 'Updated Ruleset', got %s", rs.Name)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rs.Rules))
	}
	if rs.Rules[0].Expression != "(cf.bot_management.score lt 20)" {
		t.Errorf("expected updated expression, got %s", rs.Rules[0].Expression)
	}
	if rs.Rules[1].Action != "log" {
		t.Errorf("expected action log, got %s", rs.Rules[1].Action)
	}
}

func TestRulesetClient_DeleteRuleset(t *testing.T) {
	var deleteCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/rs-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		deleteCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	})

	client := newTestRulesetClient(t, mux)
	err := client.DeleteRuleset(context.Background(), "zone-1", "rs-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleteCalled {
		t.Error("expected delete endpoint to be called")
	}
}

func TestRulesetClient_GetRuleset_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/rulesets/rs-missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 7003, "message": "Could not route to /zones/zone-1/rulesets/rs-missing, perhaps your object identifier is invalid?"}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	})

	client := newTestRulesetClient(t, mux)
	_, err := client.GetRuleset(context.Background(), "zone-1", "rs-missing")
	if err == nil {
		t.Error("expected error for missing ruleset")
	}
}
