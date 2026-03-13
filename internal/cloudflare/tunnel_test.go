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

// newTestTunnelClient creates a TunnelClient backed by a test HTTP server.
func newTestTunnelClient(t *testing.T, handler http.Handler) TunnelClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfClient := cfgo.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
	)
	return NewTunnelClientFromCF(cfClient)
}

func TestTunnelClient_CreateTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["name"] != "my-tunnel" {
			t.Errorf("expected name my-tunnel, got %v", body["name"])
		}
		if body["tunnel_secret"] != "c2VjcmV0LXZhbHVlLXRoYXQtaXMtYXQtbGVhc3QtMzItYnl0ZXM=" {
			t.Errorf("expected tunnel_secret, got %v", body["tunnel_secret"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":   "tunnel-uuid-1",
			"name": "my-tunnel",
		}))
	})

	client := newTestTunnelClient(t, mux)
	tunnel, err := client.CreateTunnel(context.Background(), "acct-1", TunnelParams{
		Name:         "my-tunnel",
		TunnelSecret: "c2VjcmV0LXZhbHVlLXRoYXQtaXMtYXQtbGVhc3QtMzItYnl0ZXM=",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.ID != "tunnel-uuid-1" {
		t.Errorf("expected ID tunnel-uuid-1, got %s", tunnel.ID)
	}
	if tunnel.Name != "my-tunnel" {
		t.Errorf("expected name my-tunnel, got %s", tunnel.Name)
	}
}

func TestTunnelClient_GetTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel/tunnel-uuid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":   "tunnel-uuid-1",
			"name": "my-tunnel",
		}))
	})

	client := newTestTunnelClient(t, mux)
	tunnel, err := client.GetTunnel(context.Background(), "acct-1", "tunnel-uuid-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tunnel.ID != "tunnel-uuid-1" {
		t.Errorf("expected ID tunnel-uuid-1, got %s", tunnel.ID)
	}
	if tunnel.Name != "my-tunnel" {
		t.Errorf("expected name my-tunnel, got %s", tunnel.Name)
	}
}

func TestTunnelClient_ListTunnelsByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		query := r.URL.Query()
		if name := query.Get("name"); name != "my-tunnel" {
			t.Errorf("expected name=my-tunnel, got %s", name)
		}
		if deleted := query.Get("is_deleted"); deleted != "false" {
			t.Errorf("expected is_deleted=false, got %s", deleted)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIListResponse(t, []map[string]any{
			{
				"id":   "tunnel-uuid-1",
				"name": "my-tunnel",
			},
			{
				"id":   "tunnel-uuid-2",
				"name": "my-tunnel",
			},
		}))
	})

	client := newTestTunnelClient(t, mux)
	tunnels, err := client.ListTunnelsByName(context.Background(), "acct-1", "my-tunnel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(tunnels))
	}
	if tunnels[0].ID != "tunnel-uuid-1" {
		t.Errorf("expected first tunnel ID tunnel-uuid-1, got %s", tunnels[0].ID)
	}
	if tunnels[1].ID != "tunnel-uuid-2" {
		t.Errorf("expected second tunnel ID tunnel-uuid-2, got %s", tunnels[1].ID)
	}
}

func TestTunnelClient_ListTunnelsByName_Empty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIListResponse(t, []map[string]any{}))
	})

	client := newTestTunnelClient(t, mux)
	tunnels, err := client.ListTunnelsByName(context.Background(), "acct-1", "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tunnels) != 0 {
		t.Errorf("expected 0 tunnels, got %d", len(tunnels))
	}
}

func TestTunnelClient_DeleteTunnel(t *testing.T) {
	var deleteCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel/tunnel-uuid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		deleteCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.Write(cfAPIResponse(t, map[string]any{
			"id":   "tunnel-uuid-1",
			"name": "my-tunnel",
		}))
	})

	client := newTestTunnelClient(t, mux)
	err := client.DeleteTunnel(context.Background(), "acct-1", "tunnel-uuid-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleteCalled {
		t.Error("expected delete endpoint to be called")
	}
}

func TestTunnelClient_GetTunnel_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct-1/cfd_tunnel/tunnel-missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 7003, "message": "Tunnel not found."}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	})

	client := newTestTunnelClient(t, mux)
	_, err := client.GetTunnel(context.Background(), "acct-1", "tunnel-missing")
	if err == nil {
		t.Error("expected error for missing tunnel")
	}
}
