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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapCache replaces the package-level clientCache with a new one built by
// newClientCache(cap, ttl) for the duration of a test.  The original is
// restored via t.Cleanup.
func swapCache(t *testing.T, cap int, ttl time.Duration) {
	t.Helper()
	orig := clientCache
	clientCache = newClientCache(cap, ttl)
	t.Cleanup(func() { clientCache = orig })
}

func TestNewClient_CacheHit_ReusesSamePointer(t *testing.T) {
	swapCache(t, 32, 30*time.Minute)

	creds := Credentials{Token: "tok-cache-hit", AccountID: "acct-cache-hit"} //nolint:gosec // G101: test fixture, not a real credential

	c1, err := NewClient(creds)
	require.NoError(t, err)
	require.NotNil(t, c1)

	c2, err := NewClient(creds)
	require.NoError(t, err)
	require.NotNil(t, c2)

	assert.Same(t, c1, c2, "second call with identical creds must return the same *Client pointer")
}

func TestNewClient_DifferentCreds_DifferentPointers(t *testing.T) {
	swapCache(t, 32, 30*time.Minute)

	credsA := Credentials{Token: "tok-A", AccountID: "acct-A"}
	credsB := Credentials{Token: "tok-B", AccountID: "acct-B"}

	cA, err := NewClient(credsA)
	require.NoError(t, err)

	cB, err := NewClient(credsB)
	require.NoError(t, err)

	assert.NotSame(t, cA, cB, "distinct creds must return distinct *Client pointers")
}

func TestNewClient_TTLExpiry_NewPointer(t *testing.T) {
	const ttl = 50 * time.Millisecond
	swapCache(t, 32, ttl)

	creds := Credentials{Token: "tok-ttl", AccountID: "acct-ttl"}

	c1, err := NewClient(creds)
	require.NoError(t, err)

	// Wait for the TTL to expire.
	time.Sleep(ttl + 20*time.Millisecond)

	c2, err := NewClient(creds)
	require.NoError(t, err)

	assert.NotSame(t, c1, c2, "after TTL expiry the cache must return a fresh *Client pointer")
}

func TestCacheKey_StableAndCollisionResistant(t *testing.T) {
	creds := Credentials{Token: "tok-stable", AccountID: "acct-stable"}

	k1 := cacheKey(creds)
	k2 := cacheKey(creds)
	assert.Equal(t, k1, k2, "cacheKey must be deterministic for identical creds")
	assert.NotEmpty(t, k1)

	// Single-byte change in Token must change the key.
	credsTokenChanged := Credentials{Token: "tok-stablX", AccountID: "acct-stable"}
	assert.NotEqual(t, k1, cacheKey(credsTokenChanged), "changing one byte of Token must change the cache key")

	// Single-byte change in AccountID must also change the key.
	credsAcctChanged := Credentials{Token: "tok-stable", AccountID: "acct-stablX"}
	assert.NotEqual(t, k1, cacheKey(credsAcctChanged), "changing one byte of AccountID must change the cache key")

	// Verify the key does not contain the raw token (it must not be decodable).
	assert.NotContains(t, k1, creds.Token.Expose(), "cacheKey must not contain the raw token")
}
