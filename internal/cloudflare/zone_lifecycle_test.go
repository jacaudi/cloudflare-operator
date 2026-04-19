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

const testZoneID = "zone-uuid-1"

func newTestZoneLifecycleClient(t *testing.T, handler http.Handler) ZoneLifecycleClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfClient := cfgo.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
	)
	return NewZoneLifecycleClientFromCF(cfClient)
}

func TestZoneLifecycleClient_CreateZone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["name"] != testRecName {
			t.Errorf("expected name example.com, got %v", body["name"])
		}
		if body["type"] != "full" {
			t.Errorf("expected type full, got %v", body["type"])
		}
		account, ok := body["account"].(map[string]any)
		if !ok {
			t.Fatal("expected account in body")
		}
		if account["id"] != "acct-1" {
			t.Errorf("expected account.id acct-1, got %v", account["id"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":                    testZoneID,
			"name":                  testRecName,
			"status":                "pending",
			"type":                  "full",
			"paused":                false,
			"name_servers":          []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
			"original_name_servers": []string{"ns1.original.com"},
			"original_registrar":    "Some Registrar, Inc.",
			"verification_key":      "abc123",
			"account":               map[string]any{"id": "acct-1", "name": "My Account"},
			"owner":                 map[string]any{},
			"plan":                  map[string]any{"id": "free"},
			"meta":                  map[string]any{},
			"activated_on":          nil,
			"created_on":            "2026-01-01T00:00:00Z",
			"modified_on":           "2026-01-01T00:00:00Z",
			"development_mode":      0,
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	zone, err := client.CreateZone(context.Background(), "acct-1", ZoneLifecycleParams{
		Name: testRecName,
		Type: "full",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if zone.ID != testZoneID {
		t.Errorf("expected ID zone-uuid-1, got %s", zone.ID)
	}
	if zone.Name != testRecName {
		t.Errorf("expected name example.com, got %s", zone.Name)
	}
	if zone.Status != "pending" {
		t.Errorf("expected status pending, got %s", zone.Status)
	}
	if len(zone.NameServers) != 2 {
		t.Errorf("expected 2 nameservers, got %d", len(zone.NameServers))
	}
	if zone.OriginalRegistrar != "Some Registrar, Inc." {
		t.Errorf("expected original registrar, got %s", zone.OriginalRegistrar)
	}
}

func TestZoneLifecycleClient_GetZone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-uuid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":                    testZoneID,
			"name":                  testRecName,
			"status":                "active",
			"type":                  "full",
			"paused":                false,
			"name_servers":          []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
			"original_name_servers": []string{"ns1.original.com"},
			"original_registrar":    "Some Registrar, Inc.",
			"verification_key":      "abc123",
			"activated_on":          "2026-01-02T00:00:00Z",
			"account":               map[string]any{"id": "acct-1", "name": "My Account"},
			"owner":                 map[string]any{},
			"plan":                  map[string]any{"id": "free"},
			"meta":                  map[string]any{},
			"created_on":            "2026-01-01T00:00:00Z",
			"modified_on":           "2026-01-02T00:00:00Z",
			"development_mode":      0,
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	zone, err := client.GetZone(context.Background(), testZoneID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if zone.ID != testZoneID {
		t.Errorf("expected ID zone-uuid-1, got %s", zone.ID)
	}
	if zone.Status != "active" {
		t.Errorf("expected status active, got %s", zone.Status)
	}
	if zone.ActivatedOn == nil {
		t.Error("expected ActivatedOn to be set")
	}
}

func TestZoneLifecycleClient_ListZonesByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		query := r.URL.Query()
		if name := query.Get("name"); name != testRecName {
			t.Errorf("expected name=example.com, got %s", name)
		}
		if acctID := query.Get("account.id"); acctID != "acct-1" {
			t.Errorf("expected account.id=acct-1, got %s", acctID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIListResponse(t, []map[string]any{
			{
				"id":                    testZoneID,
				"name":                  testRecName,
				"status":                "active",
				"type":                  "full",
				"paused":                false,
				"name_servers":          []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
				"original_name_servers": nil,
				"original_registrar":    nil,
				"activated_on":          "2026-01-02T00:00:00Z",
				"account":               map[string]any{"id": "acct-1", "name": "My Account"},
				"owner":                 map[string]any{},
				"plan":                  map[string]any{"id": "free"},
				"meta":                  map[string]any{},
				"created_on":            "2026-01-01T00:00:00Z",
				"modified_on":           "2026-01-02T00:00:00Z",
				"development_mode":      0,
			},
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	zones, err := client.ListZonesByName(context.Background(), "acct-1", testRecName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(zones) != 1 {
		t.Fatalf("expected 1 zone, got %d", len(zones))
	}
	if zones[0].ID != testZoneID {
		t.Errorf("expected zone-uuid-1, got %s", zones[0].ID)
	}
}

func TestZoneLifecycleClient_ListZonesByName_Empty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIListResponse(t, []map[string]any{}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	zones, err := client.ListZonesByName(context.Background(), "acct-1", "nonexistent.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(zones) != 0 {
		t.Errorf("expected 0 zones, got %d", len(zones))
	}
}

func TestZoneLifecycleClient_EditZone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-uuid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		paused, ok := body["paused"].(bool)
		if !ok || !paused {
			t.Errorf("expected paused=true, got %v", body["paused"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":                    testZoneID,
			"name":                  testRecName,
			"status":                "active",
			"type":                  "full",
			"paused":                true,
			"name_servers":          []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
			"original_name_servers": nil,
			"original_registrar":    nil,
			"activated_on":          "2026-01-02T00:00:00Z",
			"account":               map[string]any{"id": "acct-1", "name": "My Account"},
			"owner":                 map[string]any{},
			"plan":                  map[string]any{"id": "free"},
			"meta":                  map[string]any{},
			"created_on":            "2026-01-01T00:00:00Z",
			"modified_on":           "2026-01-03T00:00:00Z",
			"development_mode":      0,
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	paused := true
	zone, err := client.EditZone(context.Background(), testZoneID, ZoneLifecycleEditParams{
		Paused: &paused,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !zone.Paused {
		t.Error("expected zone to be paused")
	}
}

func TestZoneLifecycleClient_DeleteZone(t *testing.T) {
	var deleteCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-uuid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		deleteCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id": testZoneID,
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	err := client.DeleteZone(context.Background(), testZoneID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleteCalled {
		t.Error("expected delete endpoint to be called")
	}
}

func TestZoneLifecycleClient_TriggerActivationCheck(t *testing.T) {
	var triggerCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-uuid-1/activation_check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		triggerCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id": testZoneID,
		}))
	})

	client := newTestZoneLifecycleClient(t, mux)
	err := client.TriggerActivationCheck(context.Background(), testZoneID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !triggerCalled {
		t.Error("expected activation check endpoint to be called")
	}
}

func TestZoneLifecycleClient_GetZone_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 1049, "message": "Zone not found."}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})

	client := newTestZoneLifecycleClient(t, mux)
	_, err := client.GetZone(context.Background(), "zone-missing")
	if err == nil {
		t.Error("expected error for missing zone")
	}
}
