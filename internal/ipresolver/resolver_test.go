package ipresolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testIP = "203.0.113.1"

func TestResolveIP_SingleProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "203.0.113.1\n")
	}))
	defer server.Close()

	r := NewResolver(WithProviders([]string{server.URL}), WithCacheTTL(0))
	ip, err := r.GetExternalIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != testIP {
		t.Errorf("expected 203.0.113.1, got %s", ip)
	}
}

func TestResolveIP_ConsensusFromMultipleProviders(t *testing.T) {
	makeServer := func(ip string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, ip+"\n")
		}))
	}

	s1 := makeServer(testIP)
	s2 := makeServer(testIP)
	s3 := makeServer("198.51.100.1")
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	r := NewResolver(WithProviders([]string{s1.URL, s2.URL, s3.URL}), WithCacheTTL(0))
	ip, err := r.GetExternalIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != testIP {
		t.Errorf("expected consensus IP 203.0.113.1, got %s", ip)
	}
}

func TestResolveIP_Caching(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = fmt.Fprint(w, "203.0.113.1\n")
	}))
	defer server.Close()

	r := NewResolver(WithProviders([]string{server.URL}), WithCacheTTL(5*time.Minute))

	ip1, err := r.GetExternalIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ip2, err := r.GetExternalIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ip1 != ip2 {
		t.Errorf("cached IP should match: %s vs %s", ip1, ip2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 HTTP call (cached), got %d", callCount)
	}
}

func TestResolveIP_AllProvidersFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	r := NewResolver(WithProviders([]string{server.URL}), WithCacheTTL(0))
	_, err := r.GetExternalIP(context.Background())
	if err == nil {
		t.Error("expected error when all providers fail")
	}
}

func TestResolveIP_TrimsWhitespace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "  203.0.113.1 \n")
	}))
	defer server.Close()

	r := NewResolver(WithProviders([]string{server.URL}), WithCacheTTL(0))
	ip, err := r.GetExternalIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != testIP {
		t.Errorf("expected trimmed IP, got %q", ip)
	}
}
