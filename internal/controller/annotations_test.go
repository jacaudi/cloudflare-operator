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

package controller

import (
	"testing"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    TargetSpec
		wantErr bool
	}{
		{
			name:  "tunnel target",
			input: "tunnel:home",
			want:  TargetSpec{Kind: TargetKindTunnel, Name: "home"},
		},
		{
			name:  "cname target",
			input: "cname:foo.example.net",
			want:  TargetSpec{Kind: TargetKindCNAME, CNAME: "foo.example.net"},
		},
		{
			name:  "address target (no value)",
			input: "address",
			want:  TargetSpec{Kind: TargetKindAddress},
		},
		{
			name:    "unknown scheme",
			input:   "widget:foo",
			wantErr: true,
		},
		{
			name:    "empty tunnel name",
			input:   "tunnel:",
			wantErr: true,
		},
		{
			name:    "empty cname target",
			input:   "cname:",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseTarget(%q) err = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got != tc.want {
				t.Errorf("ParseTarget(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestMergeAnnotations(t *testing.T) {
	gw := map[string]string{
		"cloudflare.io/target":   "tunnel:home",
		"cloudflare.io/zone-ref": "example-com",
	}
	route := map[string]string{
		"cloudflare.io/zone-ref": "other-zone",
		"cloudflare.io/proxied":  "true",
	}
	want := map[string]string{
		"cloudflare.io/target":   "tunnel:home",
		"cloudflare.io/zone-ref": "other-zone",
		"cloudflare.io/proxied":  "true",
	}
	got := MergeCloudflareAnnotations(gw, route)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
	gw["unrelated.annotation"] = "noise"
	route["another.unrelated"] = "noise"
	got = MergeCloudflareAnnotations(gw, route)
	if _, ok := got["unrelated.annotation"]; ok {
		t.Error("unrelated Gateway annotations must not be copied")
	}
	if _, ok := got["another.unrelated"]; ok {
		t.Error("unrelated Route annotations must not be copied")
	}
}
