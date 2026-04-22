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
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestTunnelRuleBackend_ValidateExactlyOne(t *testing.T) {
	tests := []struct {
		name    string
		backend TunnelRuleBackend
		wantOK  bool
	}{
		{
			name:    "serviceRef only",
			backend: TunnelRuleBackend{ServiceRef: &TunnelRuleServiceRef{Name: "svc", Port: intstr.FromInt(80)}},
			wantOK:  true,
		},
		{
			name:    "url only",
			backend: TunnelRuleBackend{URL: ptr("https://foo")},
			wantOK:  true,
		},
		{
			name:    "httpStatus only",
			backend: TunnelRuleBackend{HTTPStatus: ptrInt(404)},
			wantOK:  true,
		},
		{
			name:    "none set",
			backend: TunnelRuleBackend{},
			wantOK:  false,
		},
		{
			name: "two set — serviceRef + url",
			backend: TunnelRuleBackend{
				ServiceRef: &TunnelRuleServiceRef{Name: "svc", Port: intstr.FromInt(80)},
				URL:        ptr("https://foo"),
			},
			wantOK: false,
		},
		{
			name: "all three set",
			backend: TunnelRuleBackend{
				ServiceRef: &TunnelRuleServiceRef{Name: "svc", Port: intstr.FromInt(80)},
				URL:        ptr("https://foo"),
				HTTPStatus: ptrInt(404),
			},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.backend.IsExactlyOne(); got != tc.wantOK {
				t.Errorf("IsExactlyOne() = %v, want %v", got, tc.wantOK)
			}
		})
	}
}

func ptr(s string) *string { return &s }
func ptrInt(i int) *int    { return &i }
