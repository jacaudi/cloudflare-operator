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
	"errors"
	"testing"
)

const testPayloadMinimal = `"heritage=external-dns,external-dns/owner=foo"`

func TestEncodeRegistryPayload(t *testing.T) {
	tests := []struct {
		name  string
		input RegistryPayload
		want  string
	}{
		{
			name: "httproute source",
			input: RegistryPayload{
				Owner:           "cloudflare-operator-prod",
				SourceKind:      "httproute",
				SourceNamespace: "apps",
				SourceName:      "myapp",
			},
			want: `"heritage=external-dns,external-dns/owner=cloudflare-operator-prod,external-dns/resource=httproute/apps/myapp"`,
		},
		{
			name: "service source",
			input: RegistryPayload{
				Owner:           "cloudflare-operator-prod",
				SourceKind:      "service",
				SourceNamespace: "selfhosted",
				SourceName:      "rickroll",
			},
			want: `"heritage=external-dns,external-dns/owner=cloudflare-operator-prod,external-dns/resource=service/selfhosted/rickroll"`,
		},
		{
			name:  "owner only, no resource — legacy external-dns shape",
			input: RegistryPayload{Owner: "external-dns-home"},
			want:  `"heritage=external-dns,external-dns/owner=external-dns-home"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodeRegistryPayload(tc.input)
			if got != tc.want {
				t.Errorf("EncodeRegistryPayload() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeRegistryPayload(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    RegistryPayload
		wantErr bool
	}{
		{
			name:  "valid httproute payload with quotes",
			input: `"heritage=external-dns,external-dns/owner=cloudflare-operator-prod,external-dns/resource=httproute/apps/myapp"`,
			want: RegistryPayload{
				Owner:           "cloudflare-operator-prod",
				SourceKind:      "httproute",
				SourceNamespace: "apps",
				SourceName:      "myapp",
			},
		},
		{
			name:  "valid payload without surrounding quotes",
			input: `heritage=external-dns,external-dns/owner=cloudflare-operator-prod,external-dns/resource=service/ns/svc`,
			want: RegistryPayload{
				Owner:           "cloudflare-operator-prod",
				SourceKind:      "service",
				SourceNamespace: "ns",
				SourceName:      "svc",
			},
		},
		{
			name:    "wrong heritage",
			input:   `"heritage=terraform,external-dns/owner=foo"`,
			wantErr: true,
		},
		{
			name:    "missing owner",
			input:   `"heritage=external-dns,external-dns/resource=service/ns/svc"`,
			wantErr: true,
		},
		{
			name:  "owner only (no resource ref) — legal for old external-dns format",
			input: `"heritage=external-dns,external-dns/owner=external-dns-home"`,
			want: RegistryPayload{
				Owner: "external-dns-home",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeRegistryPayload(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DecodeRegistryPayload() err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrRegistryMalformed) {
					t.Errorf("expected ErrRegistryMalformed, got %v", err)
				}
				return
			}
			if got != tc.want {
				t.Errorf("DecodeRegistryPayload() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAffixName(t *testing.T) {
	tests := []struct {
		name                string
		fqdn                string
		recordType          string
		prefix              string
		suffix              string
		wildcardReplacement string
		want                string
	}{
		{
			name:       "A record default affix",
			fqdn:       "app.example.com",
			recordType: "A",
			want:       "a-app.example.com",
		},
		{
			name:       "CNAME default affix",
			fqdn:       "app.example.com",
			recordType: "CNAME",
			want:       "cname-app.example.com",
		},
		{
			name:       "AAAA uses a- prefix (external-dns parity)",
			fqdn:       "app.example.com",
			recordType: "AAAA",
			want:       "a-app.example.com",
		},
		{
			name:       "prefix overrides default",
			fqdn:       "app.example.com",
			recordType: "A",
			prefix:     "extdns-",
			want:       "extdns-app.example.com",
		},
		{
			name:       "suffix style",
			fqdn:       "app.example.com",
			recordType: "A",
			suffix:     "-extdns",
			want:       "app-extdns.example.com",
		},
		{
			name:                "wildcard replacement",
			fqdn:                "*.example.com",
			recordType:          "A",
			wildcardReplacement: "any",
			want:                "a-any.example.com",
		},
		{
			name:       "apex record (no leaf to prefix)",
			fqdn:       "example.com",
			recordType: "A",
			want:       "a-example.com",
		},
		{
			name:       "TXT record uses lowercased type prefix",
			fqdn:       "foo.example.com",
			recordType: "TXT",
			want:       "txt-foo.example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AffixName(tc.fqdn, tc.recordType, AffixConfig{
				Prefix:              tc.prefix,
				Suffix:              tc.suffix,
				WildcardReplacement: tc.wildcardReplacement,
			})
			if got != tc.want {
				t.Errorf("AffixName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEncryptDecryptPayload(t *testing.T) {
	key32 := []byte("01234567890123456789012345678901") // 32 bytes
	plaintext := `"heritage=external-dns,external-dns/owner=cloudflare-operator-prod,external-dns/resource=service/ns/svc"`

	encoded, err := EncryptPayload(plaintext, key32)
	if err != nil {
		t.Fatalf("EncryptPayload() err = %v", err)
	}
	if encoded == plaintext {
		t.Fatalf("encrypted payload equals plaintext")
	}

	got, err := DecryptPayload(encoded, [][]byte{key32})
	if err != nil {
		t.Fatalf("DecryptPayload() err = %v", err)
	}
	if got != plaintext {
		t.Errorf("DecryptPayload() = %q, want %q", got, plaintext)
	}
}

func TestDecryptPayload_PlaintextPassthrough(t *testing.T) {
	got, err := DecryptPayload(testPayloadMinimal, nil)
	if err != nil {
		t.Fatalf("DecryptPayload() on plaintext err = %v", err)
	}
	if got != testPayloadMinimal {
		t.Errorf("got %q, want %q (plaintext should pass through)", got, testPayloadMinimal)
	}
}

func TestDecryptPayload_TriesKeysInOrder(t *testing.T) {
	keyOld := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	keyNew := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	encoded, err := EncryptPayload(testPayloadMinimal, keyOld)
	if err != nil {
		t.Fatalf("EncryptPayload() err = %v", err)
	}

	got, err := DecryptPayload(encoded, [][]byte{keyNew, keyOld})
	if err != nil {
		t.Fatalf("DecryptPayload() err = %v", err)
	}
	if got != testPayloadMinimal {
		t.Errorf("DecryptPayload() = %q, want %q", got, testPayloadMinimal)
	}
}

func TestDecryptPayload_AllKeysFail(t *testing.T) {
	realKey := []byte("cccccccccccccccccccccccccccccccc")
	wrongKey := []byte("dddddddddddddddddddddddddddddddd")

	encoded, _ := EncryptPayload(testPayloadMinimal, realKey)

	_, err := DecryptPayload(encoded, [][]byte{wrongKey})
	if err == nil {
		t.Fatalf("DecryptPayload() with wrong key should error, got nil")
	}
}
