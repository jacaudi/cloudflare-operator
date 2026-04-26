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

// Package config provides startup configuration loading for the
// cloudflare-operator. It reads environment variables and an optional
// Kubernetes Secret to produce a controller.RegistryConfig that is
// threaded into the DNS, ServiceSource, and HTTPRouteSource controllers.
package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/controller"
)

// Sentinel errors for classifiable failure cases.
// Callers MUST use errors.Is for comparison — never compare error strings.
var (
	// ErrTxtOwnerIDRequired is returned when TXT_OWNER_ID is unset or empty.
	// Without an owner ID, the annotation-driven sources cannot function.
	ErrTxtOwnerIDRequired = errors.New("TXT_OWNER_ID is required to activate annotation-driven sources")

	// ErrSelfImport is returned when TxtImportOwners contains the same value
	// as TxtOwnerID, which would cause the operator to try to adopt its own
	// records on every reconcile.
	ErrSelfImport = errors.New("TxtImportOwners must not contain TxtOwnerID")

	// ErrInvalidAESKey is returned when a key decoded from the Secret is not
	// exactly 32 bytes (required for AES-256), or cannot be base64-decoded.
	ErrInvalidAESKey = errors.New("invalid AES-256 key length")
)

// LoadOptions holds the raw values — typically from environment variables and
// command-line flags — used to construct a RegistryConfig. cmd/main.go reads
// env vars and passes them here so this package stays testable without
// os.Getenv calls.
type LoadOptions struct {
	// TxtOwnerID is the value of TXT_OWNER_ID. Required; empty triggers
	// ErrTxtOwnerIDRequired.
	TxtOwnerID string

	// TxtImportOwners is a comma-separated list of owner IDs (e.g.
	// "external-dns,legacy-operator") that this operator is allowed to
	// adopt. Maps to TXT_IMPORT_OWNERS env var.
	TxtImportOwners string

	// TxtPrefix maps to TXT_PREFIX env var.
	TxtPrefix string

	// TxtSuffix maps to TXT_SUFFIX env var.
	TxtSuffix string

	// TxtWildcardReplacement maps to TXT_WILDCARD_REPLACEMENT env var.
	TxtWildcardReplacement string

	// SecretName is the name of the Kubernetes Secret holding optional
	// AES-256 keys. When empty, no Secret is read and the plaintext-default
	// path is taken (the most common deployment).
	SecretName string

	// SecretNamespace is the namespace of the above Secret.
	SecretNamespace string
}

// LoadRegistryConfig constructs a controller.RegistryConfig from opts and,
// when opts.SecretName is non-empty, reads the named Secret via c to load
// optional AES-256 keys.
//
// The primary (most common) path is: TxtOwnerID set, SecretName empty — this
// produces a plaintext-default config with no encryption keys. Annotation-
// driven sources work fully on this path.
//
// The encryption path (SecretName set) is hidden infrastructure: the code
// ships and is exercised by tests, but operator documentation does not surface
// it in v1. Set encryptKey and/or importKeys in the Secret to enable it.
//
// When opts.TxtOwnerID is empty, ErrTxtOwnerIDRequired is returned. When
// opts.TxtImportOwners contains TxtOwnerID, ErrSelfImport is returned. When
// a key in the Secret is malformed, ErrInvalidAESKey is returned (wrapped).
//
// c must be an API-reader (mgr.GetAPIReader()) to bypass the cache, which may
// not be populated at operator startup.
func LoadRegistryConfig(ctx context.Context, c client.Reader, opts LoadOptions) (controller.RegistryConfig, error) {
	if opts.TxtOwnerID == "" {
		return controller.RegistryConfig{}, fmt.Errorf("%w", ErrTxtOwnerIDRequired)
	}

	importOwners := SplitCSV(opts.TxtImportOwners)
	if slices.Contains(importOwners, opts.TxtOwnerID) {
		return controller.RegistryConfig{}, fmt.Errorf("%w: %q is the local owner ID", ErrSelfImport, opts.TxtOwnerID)
	}

	cfg := controller.RegistryConfig{
		TxtOwnerID:      opts.TxtOwnerID,
		TxtImportOwners: importOwners,
		AffixConfig: cfclient.AffixConfig{
			Prefix:              opts.TxtPrefix,
			Suffix:              opts.TxtSuffix,
			WildcardReplacement: opts.TxtWildcardReplacement,
		},
	}

	if opts.SecretName == "" {
		return cfg, nil
	}

	var sec corev1.Secret
	key := client.ObjectKey{Namespace: opts.SecretNamespace, Name: opts.SecretName}
	if err := c.Get(ctx, key, &sec); err != nil {
		return controller.RegistryConfig{}, fmt.Errorf("load registry secret %s/%s: %w", opts.SecretNamespace, opts.SecretName, err)
	}

	if raw, ok := sec.Data["encryptKey"]; ok && len(raw) > 0 {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return controller.RegistryConfig{}, fmt.Errorf("%w: encryptKey: base64 decode: %w", ErrInvalidAESKey, err)
		}
		if len(decoded) != 32 {
			return controller.RegistryConfig{}, fmt.Errorf("%w: encryptKey must be 32 bytes (AES-256), got %d", ErrInvalidAESKey, len(decoded))
		}
		cfg.TxtEncryptAESKey = decoded
	}

	if raw, ok := sec.Data["importKeys"]; ok && len(raw) > 0 {
		keys, err := parseImportKeys(raw)
		if err != nil {
			return controller.RegistryConfig{}, err
		}
		cfg.TxtImportDecryptKeys = keys
	}

	return cfg, nil
}

// parseImportKeys decodes a newline-separated list of base64-encoded AES-256
// keys from raw. Blank lines (including lines containing only whitespace) are
// silently skipped. Non-base64 or wrong-length entries return ErrInvalidAESKey.
func parseImportKeys(raw []byte) ([][]byte, error) {
	var keys [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("%w: importKeys: base64 decode: %w", ErrInvalidAESKey, err)
		}
		if len(decoded) != 32 {
			return nil, fmt.Errorf("%w: importKeys entry must be 32 bytes (AES-256), got %d", ErrInvalidAESKey, len(decoded))
		}
		keys = append(keys, decoded)
	}
	return keys, nil
}

// SplitCSV splits a comma-separated string into a slice of trimmed, non-empty
// tokens. Returns nil when s is empty or contains only blank tokens.
// Exported so that cmd/main.go — and tests in the config_test package — can
// access it directly.
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
