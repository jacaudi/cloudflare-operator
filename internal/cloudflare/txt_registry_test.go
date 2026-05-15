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
