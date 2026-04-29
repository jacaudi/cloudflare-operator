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

func TestRuleLogging_RoundTrip(t *testing.T) {
	en := true
	in := RuleLogging{Enabled: &en}
	got, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"enabled":true}` {
		t.Errorf("got %s", got)
	}
	empty, err := json.Marshal(RuleLogging{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty: got %s", empty)
	}
}

func TestRulesetRuleSpec_LoggingOmitEmpty(t *testing.T) {
	r := RulesetRuleSpec{Action: "skip", Expression: "true"}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got == nil || len(got) == 0 || containsKey(got, "logging") {
		t.Errorf("logging should be omitted when nil; got %s", got)
	}
}

func containsKey(b []byte, k string) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, ok := m[k]
	return ok
}
