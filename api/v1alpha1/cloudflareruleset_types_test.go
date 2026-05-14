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

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestRuleset_PhaseRequired(t *testing.T) {
	r := CloudflareRuleset{Spec: CloudflareRulesetSpec{
		ZoneID: "z", Name: "waf", Phase: "http_request_firewall_custom",
		Rules: []RulesetRuleSpec{{Action: "block", Expression: `(ip.src eq 192.0.2.4)`}},
	}}
	require.Equal(t, "http_request_firewall_custom", r.Spec.Phase)
	require.Len(t, r.Spec.Rules, 1)
	require.Equal(t, "block", r.Spec.Rules[0].Action)
}

func TestRuleset_RuleLogging(t *testing.T) {
	on := true
	rl := RuleLogging{Enabled: &on}
	require.NotNil(t, rl.Enabled)
	require.True(t, *rl.Enabled)
}

func TestRuleset_ActionParametersJSON(t *testing.T) {
	rule := RulesetRuleSpec{
		Action:           "execute",
		Expression:       "true",
		ActionParameters: &apiextensionsv1.JSON{Raw: []byte(`{"id":"managed-1"}`)},
	}
	require.NotNil(t, rule.ActionParameters)
}
