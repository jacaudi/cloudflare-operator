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

package config_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jacaudi/cloudflare-operator/internal/config"
)

// ---- test helpers ------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return s
}

func newSecret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: data,
	}
}

// validAESKey returns a 32-byte key, base64-encoded for use in Secret data.
func validAESKeyB64() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func validAESKeyBytes() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

// ---- splitCSV tests ----------------------------------------------------------

func TestSplitCSV_EmptyString(t *testing.T) {
	got := config.SplitCSV("")
	if got != nil {
		t.Errorf("SplitCSV(\"\") = %v, want nil", got)
	}
}

func TestSplitCSV_SingleItem(t *testing.T) {
	got := config.SplitCSV("a")
	want := []string{"a"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("SplitCSV(\"a\") = %v, want %v", got, want)
	}
}

func TestSplitCSV_MultipleItems(t *testing.T) {
	got := config.SplitCSV("a,b,c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("SplitCSV len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitCSV_WhitespaceAndBlanks(t *testing.T) {
	got := config.SplitCSV("  a , b ,,")
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("SplitCSV(whitespace) len = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitCSV_TrailingComma(t *testing.T) {
	got := config.SplitCSV("a,b,")
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("SplitCSV(trailing comma) len = %d, want %d; got %v", len(got), len(want), got)
	}
}

// ---- LoadRegistryConfig tests ------------------------------------------------

// TestLoadRegistryConfig_TxtOwnerIDRequired verifies that when TXT_OWNER_ID env
// var is empty (not set) and no override is provided, LoadRegistryConfig returns
// ErrTxtOwnerIDRequired — the caller (main.go) passes env vars explicitly.
func TestLoadRegistryConfig_EmptyOwnerIDReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "",
		SecretName:      "",
		SecretNamespace: "",
	}

	_, err := config.LoadRegistryConfig(ctx, c, opts)
	if !errors.Is(err, config.ErrTxtOwnerIDRequired) {
		t.Errorf("want ErrTxtOwnerIDRequired, got %v", err)
	}
}

// TestLoadRegistryConfig_PlaintextDefault is the PRIMARY PATH — TXT_OWNER_ID
// set, no Secret reference, no encryption. Most users will hit this path.
func TestLoadRegistryConfig_PlaintextDefault(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "",
		SecretNamespace: "",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TxtOwnerID != "cloudflare-operator" {
		t.Errorf("TxtOwnerID = %q, want %q", cfg.TxtOwnerID, "cloudflare-operator")
	}
	if len(cfg.TxtEncryptAESKey) != 0 {
		t.Errorf("TxtEncryptAESKey should be empty for plaintext default, got len=%d", len(cfg.TxtEncryptAESKey))
	}
	if len(cfg.TxtImportDecryptKeys) != 0 {
		t.Errorf("TxtImportDecryptKeys should be empty for plaintext default, got len=%d", len(cfg.TxtImportDecryptKeys))
	}
}

// TestLoadRegistryConfig_AffixEnvVars verifies TxtPrefix/TxtSuffix/
// TxtWildcardReplacement are threaded into cfg.AffixConfig correctly.
func TestLoadRegistryConfig_AffixEnvVars(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:             "my-operator",
		TxtPrefix:              "pfx-",
		TxtSuffix:              "-sfx",
		TxtWildcardReplacement: "star",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AffixConfig.Prefix != "pfx-" {
		t.Errorf("Prefix = %q, want %q", cfg.AffixConfig.Prefix, "pfx-")
	}
	if cfg.AffixConfig.Suffix != "-sfx" {
		t.Errorf("Suffix = %q, want %q", cfg.AffixConfig.Suffix, "-sfx")
	}
	if cfg.AffixConfig.WildcardReplacement != "star" {
		t.Errorf("WildcardReplacement = %q, want %q", cfg.AffixConfig.WildcardReplacement, "star")
	}
}

// TestLoadRegistryConfig_TxtImportOwners verifies CSV import owners are parsed
// and that specifying TxtOwnerID in TxtImportOwners returns ErrSelfImport.
func TestLoadRegistryConfig_TxtImportOwners(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		TxtImportOwners: "external-dns,legacy-dns",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TxtImportOwners) != 2 {
		t.Fatalf("TxtImportOwners len = %d, want 2; got %v", len(cfg.TxtImportOwners), cfg.TxtImportOwners)
	}
}

// TestLoadRegistryConfig_SelfImportReturnsError verifies that including
// TxtOwnerID in TxtImportOwners returns ErrSelfImport.
func TestLoadRegistryConfig_SelfImportReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		TxtImportOwners: "cloudflare-operator,external-dns",
	}

	_, err := config.LoadRegistryConfig(ctx, c, opts)
	if !errors.Is(err, config.ErrSelfImport) {
		t.Errorf("want ErrSelfImport, got %v", err)
	}
}

// TestLoadRegistryConfig_SecretMissing verifies that a non-empty SecretName
// that doesn't exist in the cluster returns a wrapped error.
func TestLoadRegistryConfig_SecretMissing(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "does-not-exist",
		SecretNamespace: "default",
	}

	_, err := config.LoadRegistryConfig(ctx, c, opts)
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

// TestLoadRegistryConfig_SecretWithoutKeys verifies that a Secret with neither
// encryptKey nor importKeys returns a config with empty key slices and no error.
func TestLoadRegistryConfig_SecretWithoutKeys(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	sec := newSecret("default", "registry-keys", map[string][]byte{
		"unrelated": []byte("data"),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TxtEncryptAESKey) != 0 {
		t.Errorf("TxtEncryptAESKey should be empty, got len=%d", len(cfg.TxtEncryptAESKey))
	}
	if len(cfg.TxtImportDecryptKeys) != 0 {
		t.Errorf("TxtImportDecryptKeys should be empty, got len=%d", len(cfg.TxtImportDecryptKeys))
	}
}

// TestLoadRegistryConfig_EncryptKeySmoke is a single smoke test covering the
// encryption-on path (gated optional infra). One round-trip test is sufficient
// since this path is hidden in v1.
func TestLoadRegistryConfig_EncryptKeySmoke(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	keyB64 := validAESKeyB64()
	sec := newSecret("default", "registry-keys", map[string][]byte{
		"encryptKey": []byte(keyB64),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantKey := validAESKeyBytes()
	if len(cfg.TxtEncryptAESKey) != 32 {
		t.Errorf("TxtEncryptAESKey len = %d, want 32", len(cfg.TxtEncryptAESKey))
	}
	for i, b := range wantKey {
		if cfg.TxtEncryptAESKey[i] != b {
			t.Errorf("TxtEncryptAESKey[%d] = %d, want %d", i, cfg.TxtEncryptAESKey[i], b)
		}
	}
}

// TestLoadRegistryConfig_ImportKeysMultiple verifies that a multi-line importKeys
// value in the Secret results in all 3 keys loaded into TxtImportDecryptKeys.
func TestLoadRegistryConfig_ImportKeysMultiple(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	key1 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	key2 := validAESKeyB64()
	key3b := make([]byte, 32)
	for i := range key3b {
		key3b[i] = byte(i + 1)
	}
	key3 := base64.StdEncoding.EncodeToString(key3b)
	importKeysVal := key1 + "\n" + key2 + "\n" + key3

	sec := newSecret("default", "registry-keys", map[string][]byte{
		"importKeys": []byte(importKeysVal),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TxtImportDecryptKeys) != 3 {
		t.Errorf("TxtImportDecryptKeys len = %d, want 3", len(cfg.TxtImportDecryptKeys))
	}
}

// TestLoadRegistryConfig_ImportKeysWhitespaceAndBlanks verifies that whitespace
// and blank lines in the importKeys field are skipped without error.
func TestLoadRegistryConfig_ImportKeysWhitespaceAndBlanks(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	key1 := validAESKeyB64()
	// blank lines and trailing newline
	importKeysVal := "\n" + key1 + "\n\n"

	sec := newSecret("default", "registry-keys", map[string][]byte{
		"importKeys": []byte(importKeysVal),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	cfg, err := config.LoadRegistryConfig(ctx, c, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TxtImportDecryptKeys) != 1 {
		t.Errorf("TxtImportDecryptKeys len = %d, want 1", len(cfg.TxtImportDecryptKeys))
	}
}

// TestLoadRegistryConfig_ImportKeysInvalidBase64 verifies that an invalid
// base64 entry in importKeys returns a wrapped ErrInvalidAESKey.
func TestLoadRegistryConfig_ImportKeysInvalidBase64(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	sec := newSecret("default", "registry-keys", map[string][]byte{
		"importKeys": []byte("not-valid-base64!!!"),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	_, err := config.LoadRegistryConfig(ctx, c, opts)
	if !errors.Is(err, config.ErrInvalidAESKey) {
		t.Errorf("want ErrInvalidAESKey, got %v", err)
	}
}

// TestLoadRegistryConfig_EncryptKeyWrongLength verifies that a 16-byte key
// (not 32) returns a wrapped ErrInvalidAESKey.
func TestLoadRegistryConfig_EncryptKeyWrongLength(t *testing.T) {
	ctx := context.Background()
	s := newScheme(t)
	// 16-byte key, valid base64 but wrong length for AES-256
	shortKeyB64 := base64.StdEncoding.EncodeToString(make([]byte, 16))
	sec := newSecret("default", "registry-keys", map[string][]byte{
		"encryptKey": []byte(shortKeyB64),
	})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sec).Build()

	opts := config.LoadOptions{
		TxtOwnerID:      "cloudflare-operator",
		SecretName:      "registry-keys",
		SecretNamespace: "default",
	}

	_, err := config.LoadRegistryConfig(ctx, c, opts)
	if !errors.Is(err, config.ErrInvalidAESKey) {
		t.Errorf("want ErrInvalidAESKey, got %v", err)
	}
}
