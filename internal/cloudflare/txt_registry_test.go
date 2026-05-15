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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryPayload_FieldShape(t *testing.T) {
	p := RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "root", H: "sha256:abc123"}
	require.Equal(t, 1, p.V)
	require.Equal(t, "CloudflareDNSRecord", p.K)
}

func TestAffixName_Apex(t *testing.T) {
	require.Equal(t, "cf-txt.test", AffixName("cf-txt", "test"))
}

func TestAffixName_Subdomain(t *testing.T) {
	require.Equal(t, "cf-txt-foo.test", AffixName("cf-txt", "foo.test"))
}

func TestAffixName_DeepSubdomain(t *testing.T) {
	require.Equal(t, "cf-txt-foo-bar.test", AffixName("cf-txt", "foo.bar.test"))
}

func TestRegistryPayload_JSONTags(t *testing.T) {
	p := RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "root"}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"v":1,"k":"CloudflareDNSRecord","ns":"default","n":"root"}`, string(b))
	p.H = "sha256:abc"
	b, err = json.Marshal(p)
	require.NoError(t, err)
	require.Contains(t, string(b), `"h":"sha256:abc"`)
}

func TestErrUnrecognizedCodec_Is(t *testing.T) {
	require.ErrorIs(t, ErrUnrecognizedCodec, ErrUnrecognizedCodec)
	require.EqualError(t, ErrUnrecognizedCodec, "txt registry: unrecognized codec or malformed payload")
}

func TestPlaintextCodec_RoundTrip(t *testing.T) {
	c := plaintextCodec{}
	p := RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "media", N: "root", H: "sha256:deadbeef"}
	encoded, err := c.Encode(p)
	require.NoError(t, err)
	got, err := c.Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, p, got)
}

func TestPlaintextCodec_RejectsUnknownVersion(t *testing.T) {
	_, err := plaintextCodec{}.Decode(`{"v":99,"k":"X","ns":"y","n":"z"}`)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnrecognizedCodec, "v=99 must wrap ErrUnrecognizedCodec for AdoptRefusedNoTXT branching")
}

func TestPlaintextCodec_RejectsMalformedJSON(t *testing.T) {
	_, err := plaintextCodec{}.Decode("not-json-at-all")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnrecognizedCodec, "malformed JSON must wrap ErrUnrecognizedCodec for AdoptRefusedNoTXT branching")
}

func TestPlaintextCodec_KindIsPlaintext(t *testing.T) {
	require.Equal(t, "plaintext", plaintextCodec{}.Kind())
}

func makeKey(t *testing.T, seed byte) [32]byte {
	t.Helper()
	var k [32]byte
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

func TestAESCodec_RoundTrip(t *testing.T) {
	c := aesCodec{key: makeKey(t, 1)}
	p := RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "media", N: "root", H: "sha256:deadbeef"}
	encoded, err := c.Encode(p)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(encoded, "v1:"), "encoded form must start with v1: prefix")
	got, err := c.Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, p, got)
}

func TestAESCodec_FreshNoncePerEncode(t *testing.T) {
	c := aesCodec{key: makeKey(t, 1)}
	p := RegistryPayload{V: 1, K: "X", NS: "ns", N: "n"}
	seen := make(map[string]bool)
	for i := 0; i < 10; i++ {
		s, err := c.Encode(p)
		require.NoError(t, err)
		require.False(t, seen[s], "Encode #%d produced a duplicate (no fresh nonce?)", i)
		seen[s] = true
	}
}

func TestAESCodec_RejectsWrongKey(t *testing.T) {
	good := aesCodec{key: makeKey(t, 1)}
	bad := aesCodec{key: makeKey(t, 99)}
	p := RegistryPayload{V: 1, K: "X", NS: "ns", N: "n"}
	encoded, err := good.Encode(p)
	require.NoError(t, err)
	_, err = bad.Decode(encoded)
	require.Error(t, err, "decoding with wrong key must fail (GCM auth tag)")
	require.ErrorIs(t, err, ErrUnrecognizedCodec, "wrong-key decode must wrap ErrUnrecognizedCodec for AdoptRefusedNoTXT branching")
}

func TestAESCodec_RejectsTampering(t *testing.T) {
	c := aesCodec{key: makeKey(t, 1)}
	p := RegistryPayload{V: 1, K: "X", NS: "ns", N: "n"}
	encoded, err := c.Encode(p)
	require.NoError(t, err)
	tampered := encoded[:len(encoded)-1] + string([]byte{encoded[len(encoded)-1] ^ 0x01})
	_, err = c.Decode(tampered)
	require.Error(t, err, "GCM auth tag should catch ciphertext tampering")
	require.ErrorIs(t, err, ErrUnrecognizedCodec)
}

func TestAESCodec_RejectsMalformedV1Format(t *testing.T) {
	c := aesCodec{key: makeKey(t, 1)}
	for _, bad := range []string{"v1", "v1:", "v1::", "v1:not-base64:also-not-base64", "v2:something:else", "random-text"} {
		_, err := c.Decode(bad)
		require.Error(t, err, "input %q should be rejected", bad)
		require.ErrorIs(t, err, ErrUnrecognizedCodec, "input %q must wrap ErrUnrecognizedCodec", bad)
	}
}

func TestAESCodec_KindIsAESGCM(t *testing.T) {
	require.Equal(t, "aes-gcm", aesCodec{key: makeKey(t, 1)}.Kind())
}
