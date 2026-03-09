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

// newTestZoneClient creates a ZoneClient backed by a test HTTP server.
func newTestZoneClient(t *testing.T, handler http.Handler) ZoneClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfClient := cfgo.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)
	return NewZoneClientFromCF(cfClient)
}

func TestZoneClient_GetSettings(t *testing.T) {
	client := newTestZoneClient(t, http.NewServeMux())
	settings, err := client.GetSettings(context.Background(), "zone-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if settings != nil {
		t.Errorf("expected nil settings, got %v", settings)
	}
}

func TestZoneClient_UpdateSetting(t *testing.T) {
	var (
		capturedMethod    string
		capturedSettingID string
		capturedBody      map[string]any
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/settings/ssl", func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedSettingID = "ssl"

		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":       "ssl",
			"value":    "full",
			"editable": true,
		}))
	})

	client := newTestZoneClient(t, mux)
	err := client.UpdateSetting(context.Background(), "zone-1", "ssl", "full")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedMethod != http.MethodPatch {
		t.Errorf("expected PATCH, got %s", capturedMethod)
	}
	if capturedSettingID != "ssl" {
		t.Errorf("expected setting ID ssl, got %s", capturedSettingID)
	}
	if capturedBody["value"] != "full" {
		t.Errorf("expected value 'full', got %v", capturedBody["value"])
	}
}

func TestZoneClient_UpdateSetting_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/settings/invalid", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 1007, "message": "Invalid setting."}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	})

	client := newTestZoneClient(t, mux)
	err := client.UpdateSetting(context.Background(), "zone-1", "invalid", "value")
	if err == nil {
		t.Error("expected error for invalid setting")
	}
}

func TestZoneClient_GetBotManagement(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/bot_management", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"enable_js":  true,
			"fight_mode": true,
		}))
	})

	client := newTestZoneClient(t, mux)
	config, err := client.GetBotManagement(context.Background(), "zone-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !config.EnableJS {
		t.Error("expected EnableJS true")
	}
	if !config.FightMode {
		t.Error("expected FightMode true")
	}
}

func TestZoneClient_GetBotManagement_Disabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/bot_management", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"enable_js":  false,
			"fight_mode": false,
		}))
	})

	client := newTestZoneClient(t, mux)
	config, err := client.GetBotManagement(context.Background(), "zone-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.EnableJS {
		t.Error("expected EnableJS false")
	}
	if config.FightMode {
		t.Error("expected FightMode false")
	}
}

func TestZoneClient_GetBotManagement_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/bot_management", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 10000, "message": "Authentication error"}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	})

	client := newTestZoneClient(t, mux)
	_, err := client.GetBotManagement(context.Background(), "zone-1")
	if err == nil {
		t.Error("expected error for API failure")
	}
}

func TestZoneClient_UpdateBotManagement(t *testing.T) {
	var capturedBody map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/bot_management", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"enable_js":  true,
			"fight_mode": true,
		}))
	})

	client := newTestZoneClient(t, mux)
	err := client.UpdateBotManagement(context.Background(), "zone-1", BotManagementConfig{
		EnableJS:  true,
		FightMode: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedBody["enable_js"] != true {
		t.Errorf("expected enable_js true, got %v", capturedBody["enable_js"])
	}
	if capturedBody["fight_mode"] != true {
		t.Errorf("expected fight_mode true, got %v", capturedBody["fight_mode"])
	}
}

func TestZoneClient_UpdateBotManagement_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/bot_management", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 10000, "message": "Authentication error"}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	})

	client := newTestZoneClient(t, mux)
	err := client.UpdateBotManagement(context.Background(), "zone-1", BotManagementConfig{
		EnableJS:  true,
		FightMode: true,
	})
	if err == nil {
		t.Error("expected error for API failure")
	}
}
