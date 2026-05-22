/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

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
