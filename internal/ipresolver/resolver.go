package ipresolver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

var DefaultProviders = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

type Option func(*Resolver)

func WithProviders(providers []string) Option {
	return func(r *Resolver) { r.providers = providers }
}

func WithCacheTTL(ttl time.Duration) Option {
	return func(r *Resolver) { r.cacheTTL = ttl }
}

func WithHTTPTimeout(timeout time.Duration) Option {
	return func(r *Resolver) {
		r.httpClient = &http.Client{Timeout: timeout}
	}
}

type Resolver struct {
	httpClient *http.Client
	providers  []string
	cacheTTL   time.Duration
	cachedIP   string
	cachedAt   time.Time
	mu         sync.RWMutex
	sf         singleflight.Group
}

func NewResolver(opts ...Option) *Resolver {
	r := &Resolver{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		providers:  DefaultProviders,
		cacheTTL:   60 * time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Resolver) GetExternalIP(ctx context.Context) (string, error) {
	r.mu.RLock()
	if r.cacheTTL > 0 && r.cachedIP != "" && time.Since(r.cachedAt) < r.cacheTTL {
		ip := r.cachedIP
		r.mu.RUnlock()
		return ip, nil
	}
	r.mu.RUnlock()

	// Coalesce concurrent cache-miss callers so only one provider fan-out runs
	// at a time. The key is fixed because there's only one resolution (external IP).
	ip, err, _ := r.sf.Do("externalIP", func() (any, error) {
		// Re-check cache under the flight — a prior flight may have just filled it.
		r.mu.RLock()
		if r.cacheTTL > 0 && r.cachedIP != "" && time.Since(r.cachedAt) < r.cacheTTL {
			cached := r.cachedIP
			r.mu.RUnlock()
			return cached, nil
		}
		r.mu.RUnlock()

		return r.resolveFromProviders(ctx)
	})
	if err != nil {
		return "", err
	}
	return ip.(string), nil
}

func (r *Resolver) resolveFromProviders(ctx context.Context) (string, error) {
	type result struct {
		ip  string
		err error
	}
	results := make(chan result, len(r.providers))

	for _, provider := range r.providers {
		go func(url string) {
			ip, err := r.queryProvider(ctx, url)
			results <- result{ip: ip, err: err}
		}(provider)
	}

	votes := make(map[string]int)
	var lastErr error
	for range r.providers {
		res := <-results
		if res.err != nil {
			lastErr = res.err
			continue
		}
		votes[res.ip]++
	}

	var bestIP string
	var bestCount int
	for ip, count := range votes {
		if count > bestCount {
			bestIP = ip
			bestCount = count
		}
	}

	if bestIP == "" {
		if lastErr != nil {
			return "", fmt.Errorf("all IP providers failed, last error: %w", lastErr)
		}
		return "", fmt.Errorf("all IP providers failed")
	}

	r.mu.Lock()
	r.cachedIP = bestIP
	r.cachedAt = time.Now()
	r.mu.Unlock()

	return bestIP, nil
}

func (r *Resolver) queryProvider(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider %s returned status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}

	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("provider %s returned invalid IP: %q", url, ip)
	}

	return ip, nil
}
