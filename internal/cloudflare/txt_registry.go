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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// RegistryPayload is the JSON schema for a TXT companion record (V=1).
// Field names are compact because Cloudflare TXT records are capped at
// 1024 bytes; every character counts when a record may carry multiple
// ownership claims. Decoders must reject payloads whose V field is not
// equal to 1.
type RegistryPayload struct {
	// V is the schema version. Only V=1 is recognised.
	V int `json:"v"`
	// K is the Kubernetes resource kind (e.g. "CloudflareDNSRecord").
	K string `json:"k"`
	// NS is the Kubernetes namespace of the owning object.
	NS string `json:"ns"`
	// N is the Kubernetes name of the owning object.
	N string `json:"n"`
	// H is an optional content hash of the owned record (sha256:<hex>).
	// Omitted when unknown or not yet computed.
	H string `json:"h,omitempty"`
}

// AffixName returns the name of the companion TXT record for a DNS
// record identified by name.
//
// Convention:
//   - apex (no '.' in name):  prefix + "." + name
//   - subdomain:              prefix + "-" + collapsed-labels + "." + tld
//
// All-but-last dotted segments of name are joined with hyphens and
// appended to prefix, then the last segment becomes the zone label.
// For example:
//
//	AffixName("cf-txt", "test")        → "cf-txt.test"
//	AffixName("cf-txt", "foo.test")    → "cf-txt-foo.test"
//	AffixName("cf-txt", "foo.bar.test") → "cf-txt-foo-bar.test"
func AffixName(prefix, name string) string {
	if !strings.ContainsRune(name, '.') {
		return prefix + "." + name
	}
	segs := strings.Split(name, ".")
	head := strings.Join(segs[:len(segs)-1], "-")
	return prefix + "-" + head + "." + segs[len(segs)-1]
}

// ErrUnrecognizedCodec is returned by codec decoders when the TXT
// record value is not a recognised format or the version field is
// unknown. Reconcilers should map this error to
// Reason=AdoptRefusedNoTXT so the record is left unowned.
var ErrUnrecognizedCodec = errors.New("txt registry: unrecognized codec or malformed payload")

// Codec encodes/decodes a RegistryPayload to/from a TXT record's content
// string. Implementations: plaintextCodec (bare JSON; default),
// aesCodec (v1:<nonce>:<ciphertext> AES-256-GCM; opt-in via key Secret),
// autoDetectingCodec (read-side dispatcher; sniffs the v1: prefix).
type Codec interface {
	Encode(payload RegistryPayload) (string, error)
	Decode(s string) (RegistryPayload, error)
	Kind() string
}

// plaintextCodec writes bare JSON; reads JSON OR rejects with
// ErrUnrecognizedCodec when the input is anything else. Used when no key
// Secret is configured (the v1alpha1 default).
type plaintextCodec struct{}

var _ Codec = plaintextCodec{}

func (plaintextCodec) Encode(p RegistryPayload) (string, error) {
	if p.V == 0 {
		p.V = 1 // default — callers always intend v1 in v1alpha1
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("plaintext encode: %w", err)
	}
	return string(b), nil
}

func (plaintextCodec) Decode(s string) (RegistryPayload, error) {
	var p RegistryPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return RegistryPayload{}, fmt.Errorf("plaintext decode: %w: %w", ErrUnrecognizedCodec, err)
	}
	if p.V != 1 {
		return RegistryPayload{}, fmt.Errorf("plaintext decode: %w: unsupported schema version %d", ErrUnrecognizedCodec, p.V)
	}
	return p, nil
}

func (plaintextCodec) Kind() string { return "plaintext" }

// aesCodec writes "v1:<base64-nonce>:<base64-ciphertext>" using AES-256-GCM
// with a 32-byte key loaded from the operator-configured Secret. The wire
// format's "v1:" prefix is intentional — it lets autoDetectingCodec
// dispatch by sniffing the first 3 bytes without parsing the rest.
type aesCodec struct {
	key [32]byte
}

var _ Codec = aesCodec{}

func (c aesCodec) Encode(p RegistryPayload) (string, error) {
	if p.V == 0 {
		p.V = 1
	}
	pt, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("aes encode marshal: %w", err)
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("aes nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, pt, nil)
	return "v1:" + base64.StdEncoding.EncodeToString(nonce) + ":" + base64.StdEncoding.EncodeToString(ct), nil
}

func (c aesCodec) Decode(s string) (RegistryPayload, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" {
		return RegistryPayload{}, fmt.Errorf("aes decode: %w: malformed v1 envelope", ErrUnrecognizedCodec)
	}
	nonce, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode nonce: %w: %w", ErrUnrecognizedCodec, err)
	}
	ct, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode ciphertext: %w: %w", ErrUnrecognizedCodec, err)
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode gcm: %w", err)
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode open: %w: %w", ErrUnrecognizedCodec, err)
	}
	var p RegistryPayload
	if err := json.Unmarshal(pt, &p); err != nil {
		return RegistryPayload{}, fmt.Errorf("aes decode unmarshal: %w: %w", ErrUnrecognizedCodec, err)
	}
	if p.V != 1 {
		return RegistryPayload{}, fmt.Errorf("aes decode: %w: unsupported schema version %d", ErrUnrecognizedCodec, p.V)
	}
	return p, nil
}

func (aesCodec) Kind() string { return "aes-gcm" }
