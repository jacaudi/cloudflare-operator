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
)

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
