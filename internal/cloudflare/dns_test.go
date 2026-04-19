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

const (
	testRecID   = "rec-1"
	testRecName = "example.com"
	testIP      = "1.2.3.4"
	testIPAlt   = "5.6.7.8"
	testSubName = "www.example.com"
)

// newTestDNSClient creates a DNSClient backed by a test HTTP server.
func newTestDNSClient(t *testing.T, handler http.Handler) DNSClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfClient := cfgo.NewClient(
		option.WithAPIToken("test-token"),
		option.WithBaseURL(server.URL),
	)
	return NewDNSClientFromCF(cfClient)
}

// cfAPIResponse is a helper to build Cloudflare-style JSON responses.
func cfAPIResponse(t *testing.T, result any) []byte {
	t.Helper()
	resp := map[string]any{
		"success":  true,
		"result":   result,
		"errors":   []any{},
		"messages": []any{},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	return data
}

// cfAPIListResponse is a helper to build Cloudflare-style list JSON responses.
func cfAPIListResponse(t *testing.T, results []map[string]any) []byte {
	t.Helper()
	resp := map[string]any{
		"success":  true,
		"result":   results,
		"errors":   []any{},
		"messages": []any{},
		"result_info": map[string]any{
			"count":       len(results),
			"page":        1,
			"per_page":    100,
			"total_count": len(results),
			"total_pages": 1,
		},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	return data
}

func TestGetRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records/rec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":      testRecID,
			"name":    testRecName,
			"type":    "A",
			"content": testIP,
			"proxied": true,
			"ttl":     1,
		}))
	})

	client := newTestDNSClient(t, mux)
	rec, err := client.GetRecord(context.Background(), "zone-1", testRecID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.ID != testRecID {
		t.Errorf("expected ID rec-1, got %s", rec.ID)
	}
	if rec.Name != testRecName {
		t.Errorf("expected name example.com, got %s", rec.Name)
	}
	if rec.Type != "A" {
		t.Errorf("expected type A, got %s", rec.Type)
	}
	if rec.Content != testIP {
		t.Errorf("expected content 1.2.3.4, got %s", rec.Content)
	}
	if !rec.Proxied {
		t.Error("expected proxied true")
	}
	if rec.TTL != 1 {
		t.Errorf("expected TTL 1, got %d", rec.TTL)
	}
}

func TestListRecordsByNameAndType(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		// Verify query parameters
		query := r.URL.Query()
		if name := query.Get("name.exact"); name != testSubName {
			t.Errorf("expected name.exact=www.example.com, got %s", name)
		}
		if typ := query.Get("type"); typ != "A" {
			t.Errorf("expected type=A, got %s", typ)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIListResponse(t, []map[string]any{
			{
				"id":      testRecID,
				"name":    testSubName,
				"type":    "A",
				"content": testIP,
				"proxied": false,
				"ttl":     3600,
			},
			{
				"id":      "rec-2",
				"name":    testSubName,
				"type":    "A",
				"content": testIPAlt,
				"proxied": false,
				"ttl":     3600,
			},
		}))
	})

	client := newTestDNSClient(t, mux)
	records, err := client.ListRecordsByNameAndType(context.Background(), "zone-1", testSubName, "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	if records[0].ID != testRecID {
		t.Errorf("expected first record ID rec-1, got %s", records[0].ID)
	}
	if records[0].Content != testIP {
		t.Errorf("expected first record content 1.2.3.4, got %s", records[0].Content)
	}
	if records[1].ID != "rec-2" {
		t.Errorf("expected second record ID rec-2, got %s", records[1].ID)
	}
	if records[1].Content != testIPAlt {
		t.Errorf("expected second record content 5.6.7.8, got %s", records[1].Content)
	}
}

func TestListRecordsByNameAndType_Empty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIListResponse(t, []map[string]any{}))
	})

	client := newTestDNSClient(t, mux)
	records, err := client.ListRecordsByNameAndType(context.Background(), "zone-1", "missing.example.com", "A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestCreateRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Decode request body to verify what we sent
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["name"] != testSubName {
			t.Errorf("expected name www.example.com, got %v", body["name"])
		}
		if body["type"] != "A" {
			t.Errorf("expected type A, got %v", body["type"])
		}
		if body["content"] != testIP {
			t.Errorf("expected content 1.2.3.4, got %v", body["content"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":      "rec-new",
			"name":    testSubName,
			"type":    "A",
			"content": testIP,
			"proxied": true,
			"ttl":     1,
		}))
	})

	client := newTestDNSClient(t, mux)
	proxied := true
	rec, err := client.CreateRecord(context.Background(), "zone-1", DNSRecordParams{
		Name:    testSubName,
		Type:    "A",
		Content: testIP,
		Proxied: &proxied,
		TTL:     1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.ID != "rec-new" {
		t.Errorf("expected ID rec-new, got %s", rec.ID)
	}
	if rec.Name != testSubName {
		t.Errorf("expected name www.example.com, got %s", rec.Name)
	}
	if !rec.Proxied {
		t.Error("expected proxied true")
	}
}

func TestUpdateRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records/rec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["content"] != testIPAlt {
			t.Errorf("expected content 5.6.7.8, got %v", body["content"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id":      testRecID,
			"name":    testSubName,
			"type":    "A",
			"content": testIPAlt,
			"proxied": false,
			"ttl":     3600,
		}))
	})

	client := newTestDNSClient(t, mux)
	rec, err := client.UpdateRecord(context.Background(), "zone-1", testRecID, DNSRecordParams{
		Name:    testSubName,
		Type:    "A",
		Content: testIPAlt,
		TTL:     3600,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.ID != testRecID {
		t.Errorf("expected ID rec-1, got %s", rec.ID)
	}
	if rec.Content != testIPAlt {
		t.Errorf("expected content 5.6.7.8, got %s", rec.Content)
	}
	if rec.TTL != 3600 {
		t.Errorf("expected TTL 3600, got %d", rec.TTL)
	}
}

func TestDeleteRecord(t *testing.T) {
	var deleteCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records/rec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		deleteCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfAPIResponse(t, map[string]any{
			"id": testRecID,
		}))
	})

	client := newTestDNSClient(t, mux)
	err := client.DeleteRecord(context.Background(), "zone-1", testRecID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deleteCalled {
		t.Error("expected delete endpoint to be called")
	}
}

func TestGetRecord_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones/zone-1/dns_records/rec-missing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		resp := map[string]any{
			"success":  false,
			"result":   nil,
			"errors":   []map[string]any{{"code": 81044, "message": "Record does not exist."}},
			"messages": []any{},
		}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})

	client := newTestDNSClient(t, mux)
	_, err := client.GetRecord(context.Background(), "zone-1", "rec-missing")
	if err == nil {
		t.Error("expected error for missing record")
	}
}
