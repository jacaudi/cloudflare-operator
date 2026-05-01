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
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

const testHostnameFoo = "foo.example.com"

// ---- TestSplitAndTrim -------------------------------------------------------

func TestSplitAndTrim_Empty(t *testing.T) {
	got := splitAndTrim("")
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestSplitAndTrim_Single(t *testing.T) {
	got := splitAndTrim(testHostnameFoo)
	if len(got) != 1 || got[0] != testHostnameFoo {
		t.Errorf("expected [foo.example.com], got %v", got)
	}
}

func TestSplitAndTrim_Multi(t *testing.T) {
	got := splitAndTrim("foo.example.com, bar.example.com,baz.example.com")
	want := []string{testHostnameFoo, "bar.example.com", "baz.example.com"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestSplitAndTrim_Whitespace(t *testing.T) {
	got := splitAndTrim("  foo.example.com  ,  bar.example.com  ")
	if len(got) != 2 || got[0] != testHostnameFoo || got[1] != "bar.example.com" {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestSplitAndTrim_TrailingComma(t *testing.T) {
	got := splitAndTrim("foo.example.com,")
	if len(got) != 1 || got[0] != testHostnameFoo {
		t.Errorf("unexpected result: %v", got)
	}
}

// ---- TestIsValidDNSName -----------------------------------------------------

func TestIsValidDNSName_Valid(t *testing.T) {
	cases := []string{
		testHostnameFoo,
		"sub.foo.example.com",
		"*.apps.example.com",
		"example.com",
		"a",
	}
	for _, h := range cases {
		if !isValidDNSName(h) {
			t.Errorf("expected valid, got invalid for %q", h)
		}
	}
}

func TestIsValidDNSName_Invalid(t *testing.T) {
	cases := []string{
		"",
		"*",
		"bad_name.example.com",
		"..double-dot.example.com",
		"-leading-dash.example.com",
		"trailing-dash-.example.com",
	}
	for _, h := range cases {
		if isValidDNSName(h) {
			t.Errorf("expected invalid, got valid for %q", h)
		}
	}
}

func TestIsValidDNSName_TooLongLabel(t *testing.T) {
	label := strings.Repeat("a", 64)
	h := label + ".example.com"
	if isValidDNSName(h) {
		t.Errorf("expected invalid for 64-char label, got valid")
	}
}

func TestIsValidDNSName_TooLongTotal(t *testing.T) {
	// Build a name > 253 chars.
	label := strings.Repeat("a", 50)
	h := label + "." + label + "." + label + "." + label + "." + label + ".com"
	if len(h) <= 253 {
		// Make it longer.
		h = strings.Repeat(label+".", 6) + "com"
	}
	if isValidDNSName(h) {
		t.Errorf("expected invalid for %d-char hostname, got valid", len(h))
	}
}

func TestIsValidDNSName_EmptyLabel(t *testing.T) {
	if isValidDNSName("foo..example.com") {
		t.Error("expected invalid for double-dot (empty label)")
	}
}

// ---- TestFirstNonEmpty ------------------------------------------------------

func TestFirstNonEmpty_FirstWins(t *testing.T) {
	got := firstNonEmpty("a", "b")
	if got != "a" {
		t.Errorf("expected a, got %q", got)
	}
}

func TestFirstNonEmpty_FallsBack(t *testing.T) {
	got := firstNonEmpty("", "b")
	if got != "b" {
		t.Errorf("expected b, got %q", got)
	}
}

func TestFirstNonEmpty_BothEmpty(t *testing.T) {
	got := firstNonEmpty("", "")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ---- TestSecretRefNamespace -------------------------------------------------

func TestSecretRefNamespace_RefNamespaceWins(t *testing.T) {
	ref := cloudflarev1alpha1.SecretReference{Name: "cf-creds", Namespace: "zones"}
	got := secretRefNamespace(ref, "apps")
	if got != "zones" {
		t.Errorf("expected zones, got %q", got)
	}
}

func TestSecretRefNamespace_FallbackUsedWhenEmpty(t *testing.T) {
	ref := cloudflarev1alpha1.SecretReference{Name: "cf-creds"}
	got := secretRefNamespace(ref, "apps")
	if got != "apps" {
		t.Errorf("expected apps, got %q", got)
	}
}

func TestSecretRefNamespace_BothEmpty(t *testing.T) {
	ref := cloudflarev1alpha1.SecretReference{Name: "cf-creds"}
	got := secretRefNamespace(ref, "")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ---- TestEmittedDNSRecordName / TestEmittedTunnelRuleName -------------------

func TestEmittedDNSRecordName_Shape(t *testing.T) {
	name := emittedDNSRecordName("httproute", "jellyfin", "app.example.com")
	// <kind>-<source-name>-<8 hex chars>
	if !regexp.MustCompile(`^httproute-jellyfin-[0-9a-f]{8}$`).MatchString(name) {
		t.Errorf("name = %q does not match expected shape", name)
	}
	// Length check: bounded.
	if len(name) > 63 {
		t.Errorf("name = %q exceeds 63 chars (DNS-1123 label limit)", name)
	}
}

func TestEmittedDNSRecordName_DistinctFQDNsHashDistinctly(t *testing.T) {
	a := emittedDNSRecordName("httproute", "jellyfin", "app.example.com")
	b := emittedDNSRecordName("httproute", "jellyfin", "api.example.com")
	if a == b {
		t.Errorf("distinct FQDNs produced same name: %q", a)
	}
}

func TestEmittedDNSRecordName_WildcardSafe(t *testing.T) {
	name := emittedDNSRecordName("httproute", "apps", "*.example.com")
	// The asterisk must NOT appear in the CR name (DNS-1123 forbids it).
	if strings.Contains(name, "*") {
		t.Errorf("name contains asterisk: %q", name)
	}
	// Must still match the expected shape.
	if !regexp.MustCompile(`^httproute-apps-[0-9a-f]{8}$`).MatchString(name) {
		t.Errorf("wildcard name shape wrong: %q", name)
	}
}

func TestEmittedDNSRecordName_DeterministicAcrossKinds(t *testing.T) {
	// Same FQDN, different source kinds: distinct names (hash input differs).
	a := emittedDNSRecordName("httproute", "myapp", "app.example.com")
	b := emittedDNSRecordName("svc", "myapp", "app.example.com")
	if a == b {
		t.Errorf("different source kinds produced same name: %q", a)
	}
}

func TestEmittedTunnelRuleName(t *testing.T) {
	if got := emittedTunnelRuleName("httproute", "jellyfin"); got != "httproute-jellyfin" {
		t.Errorf("got %q", got)
	}
	if got := emittedTunnelRuleName("svc", "myapp"); got != "svc-myapp" {
		t.Errorf("got %q", got)
	}
}

// ---- TestOwnerRefsFor -------------------------------------------------------

func TestOwnerRefsFor(t *testing.T) {
	obj := &cloudflarev1alpha1.CloudflareDNSRecord{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudflare.io/v1alpha1",
			Kind:       "CloudflareDNSRecord",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-record",
			Namespace: "apps",
			UID:       types.UID("svc-uid"),
		},
	}

	refs := ownerRefsFor(obj)
	if len(refs) != 1 {
		t.Fatalf("expected 1 owner ref, got %d", len(refs))
	}
	ref := refs[0]
	if ref.Name != "my-record" {
		t.Errorf("Name: expected my-record, got %q", ref.Name)
	}
	if ref.UID != "svc-uid" {
		t.Errorf("UID: expected svc-uid, got %q", ref.UID)
	}
	if ref.Kind != "CloudflareDNSRecord" {
		t.Errorf("Kind: expected CloudflareDNSRecord, got %q", ref.Kind)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("Controller should be true")
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Error("BlockOwnerDeletion should be true")
	}
}
