/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	clientCacheCap = 32
	clientCacheTTL = 30 * time.Minute
)

// clientCache stores *Client values keyed by cacheKey(creds).
// 32-entry cap + 30-minute absolute TTL (golang-lru/v2/expirable.Get does
// NOT refresh ExpiresAt on lookup, so the lifetime is measured from
// insertion, not last use).  Thread-safe per golang-lru/v2 docs.
var clientCache = newClientCache(clientCacheCap, clientCacheTTL)

// newClientCache constructs an expirable LRU for *Client.  cap and ttl are
// parameterised so tests can inject a small TTL without touching production
// constants.
func newClientCache(cap int, ttl time.Duration) *lru.LRU[string, *Client] {
	return lru.NewLRU[string, *Client](cap, nil, ttl)
}

// cacheKey derives a collision-resistant LRU key from credentials.  The key
// is a hex-encoded sha256 over the raw token bytes, a NUL separator, and the
// accountID.  The token is never stored in the key in any decodable form.
func cacheKey(creds Credentials) string {
	h := sha256.New()
	h.Write([]byte(creds.Token.Expose()))
	h.Write([]byte{0x00})
	h.Write([]byte(creds.AccountID))
	return hex.EncodeToString(h.Sum(nil))
}
