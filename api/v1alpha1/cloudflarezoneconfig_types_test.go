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

package v1alpha1

import (
	"encoding/json"
	"testing"
)

// ptrInt is defined in cloudflaretunnelrule_types_test.go; ptrBool/ptrStr
// are package-test helpers added here for the new pointer-typed fields.
func ptrBool(b bool) *bool    { return &b }
func ptrStr(s string) *string { return &s }

func TestSecurityHeaderSettings_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   SecurityHeaderSettings
		want string
	}{
		{
			name: "all fields set",
			in: SecurityHeaderSettings{
				Enabled:           ptrBool(true),
				MaxAge:            ptrInt(31536000),
				IncludeSubdomains: ptrBool(true),
				Preload:           ptrBool(false),
				Nosniff:           ptrBool(true),
			},
			want: `{"enabled":true,"maxAge":31536000,"includeSubdomains":true,"preload":false,"nosniff":true}`,
		},
		{
			name: "all fields nil",
			in:   SecurityHeaderSettings{},
			want: `{}`,
		},
		{
			name: "partial — only Enabled=false",
			in:   SecurityHeaderSettings{Enabled: ptrBool(false)},
			want: `{"enabled":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("marshal: got %s, want %s", got, tc.want)
			}
			var back SecurityHeaderSettings
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		})
	}
}

func TestDNSSettings_RoundTrip(t *testing.T) {
	in := DNSSettings{CNAMEFlattening: ptrStr("flatten_at_root")}
	got, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"cnameFlattening":"flatten_at_root"}` {
		t.Errorf("got %s", got)
	}
	empty, err := json.Marshal(DNSSettings{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty: got %s, want {}", empty)
	}
}

func TestSecuritySettings_NewFieldsOmitEmpty(t *testing.T) {
	s := SecuritySettings{ServerSideExclude: ptrStr("on")}
	got, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"serverSideExclude":"on"}` {
		t.Errorf("got %s, want only the configured field", got)
	}
}

func TestPerformanceSettings_NewFieldsOmitEmpty(t *testing.T) {
	p := PerformanceSettings{RocketLoader: ptrStr("on")}
	got, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"rocketLoader":"on"}` {
		t.Errorf("got %s", got)
	}
}
