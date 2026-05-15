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

package zone

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrTxtRegistryKeyMalformed is returned by loadCodec when the Secret's key
// data is present but not exactly 32 bytes, which is required for AES-256-GCM.
var ErrTxtRegistryKeyMalformed = errors.New("txt registry key must be exactly 32 bytes")

// txtAffix is the prefix used to derive TXT companion record names via
// cloudflare.AffixName. It identifies records written by this operator.
const txtAffix = "cf-txt"

// TxtOwnership classifies the ownership relationship between a TXT companion
// record and the CloudflareDNSRecord that queries it.
type TxtOwnership int

const (
	// TxtOwnershipMatch means the TXT record was written by this exact CR.
	TxtOwnershipMatch TxtOwnership = iota
	// TxtOwnershipForeign means the TXT record exists but belongs to a
	// different CR (different namespace/name or kind).
	TxtOwnershipForeign
	// TxtOwnershipUnrecognized means the TXT record content could not be
	// decoded by the configured codec.
	TxtOwnershipUnrecognized
	// TxtOwnershipAbsent means no TXT companion record exists for the name.
	TxtOwnershipAbsent
)

// loadCodec resolves the codec to use for TXT companion records. When keyRef
// is nil or has an empty Name, the plaintext codec is returned. Otherwise the
// referenced Secret is fetched; if absent the error wraps ErrSecretNotFound,
// if the named key is missing it wraps ErrSecretKeyMissing, and if the key
// material is not exactly 32 bytes it wraps ErrTxtRegistryKeyMalformed.
//
// The key name inside the Secret defaults to "key" (not "token") when
// keyRef.Key is empty — the TXT key Secret uses a different default than the
// credential Secret.
func loadCodec(ctx context.Context, c client.Client, keyRef *v1alpha1.SecretReference, defaultNamespace string) (cloudflare.Codec, error) {
	if keyRef == nil || keyRef.Name == "" {
		return cloudflare.NewPlaintextCodec(), nil
	}

	ns := keyRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	keyName := keyRef.Key
	if keyName == "" {
		keyName = "key"
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: keyRef.Name, Namespace: ns}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("txt registry key Secret %s/%s: %w", ns, keyRef.Name, cloudflare.ErrSecretNotFound)
		}
		return nil, fmt.Errorf("txt registry key Secret %s/%s: %w", ns, keyRef.Name, err)
	}

	raw, ok := secret.Data[keyName]
	if !ok {
		return nil, fmt.Errorf("txt registry key Secret %s/%s missing key %q: %w", ns, keyRef.Name, keyName, cloudflare.ErrSecretKeyMissing)
	}

	if len(raw) != 32 {
		return nil, fmt.Errorf("txt registry key Secret %s/%s key %q has %d bytes: %w", ns, keyRef.Name, keyName, len(raw), ErrTxtRegistryKeyMalformed)
	}

	var key [32]byte
	copy(key[:], raw)
	return cloudflare.NewAESCodec(key), nil
}

// autoDetectingFor returns a read-side codec that can decode both plaintext
// and encrypted TXT records. It wraps the configured encoder so that existing
// records written with either format can be read back correctly.
func autoDetectingFor(encoder cloudflare.Codec) cloudflare.Codec {
	if encoder.Kind() == "aes-gcm" {
		return cloudflare.NewAutoDetectingCodec(encoder)
	}
	return cloudflare.NewAutoDetectingCodec(nil)
}

// verifyTXTOwnership decodes a TXT companion record's content using the
// supplied codec and classifies ownership relative to the caller's identity.
func verifyTXTOwnership(txtContent string, codec cloudflare.Codec, ourKind, ourNS, ourName string) TxtOwnership {
	p, err := codec.Decode(txtContent)
	if err != nil {
		return TxtOwnershipUnrecognized
	}
	if p.K == ourKind && p.NS == ourNS && p.N == ourName {
		return TxtOwnershipMatch
	}
	return TxtOwnershipForeign
}

// writeTXTCompanion creates or updates the TXT companion record for the given
// DNS record name in Cloudflare. It encodes a RegistryPayload with the
// provided identity fields and content hash, then upserts the record under the
// affixed name. The ID of the resulting TXT record is returned.
func writeTXTCompanion(
	ctx context.Context,
	dc cloudflare.DNSClient,
	zoneID, recordName, contentHash string,
	ourNS, ourName string,
	encoder cloudflare.Codec,
) (string, error) {
	txtName := cloudflare.AffixName(txtAffix, recordName)

	payload := cloudflare.RegistryPayload{
		V:  1,
		K:  "CloudflareDNSRecord",
		NS: ourNS,
		N:  ourName,
		H:  contentHash,
	}
	content, err := encoder.Encode(payload)
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion encode: %w", err)
	}

	existing, err := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion list: %w", err)
	}

	if len(existing) > 0 {
		updated, err := dc.UpdateRecord(ctx, zoneID, existing[0].ID, cloudflare.DNSRecordParams{
			Type:    "TXT",
			Name:    txtName,
			Content: content,
			TTL:     1,
		})
		if err != nil {
			return "", fmt.Errorf("writeTXTCompanion update: %w", err)
		}
		return updated.ID, nil
	}

	created, err := dc.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type:    "TXT",
		Name:    txtName,
		Content: content,
		TTL:     1,
	})
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion create: %w", err)
	}
	return created.ID, nil
}

// deleteTXTCompanion removes the TXT companion record with the given Cloudflare
// record ID. An empty txtRecordID is treated as a no-op. ErrRecordNotFound is
// tolerated (the record may already have been removed externally).
func deleteTXTCompanion(ctx context.Context, dc cloudflare.DNSClient, zoneID, txtRecordID string) error {
	if txtRecordID == "" {
		return nil
	}
	if err := dc.DeleteRecord(ctx, zoneID, txtRecordID); err != nil {
		if errors.Is(err, cloudflare.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("deleteTXTCompanion: %w", err)
	}
	return nil
}
