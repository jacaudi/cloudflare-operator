/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
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
