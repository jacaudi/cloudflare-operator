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
	"errors"
	"testing"

	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// helpers

func ownPayload(owner string) string {
	return cfclient.EncodeRegistryPayload(cfclient.RegistryPayload{
		Owner:           owner,
		SourceKind:      "httproute",
		SourceNamespace: "default",
		SourceName:      "my-route",
	})
}

// TestRegistryDecision covers all six plan cases plus additional edge cases.
func TestRegistryDecision(t *testing.T) {
	const ownerID = "cloudflare-operator"

	baseCfg := RegistryConfig{
		TxtOwnerID: ownerID,
	}

	tests := []struct {
		name               string
		cfg                RegistryConfig
		existingTXTContent string              // "" = no companion TXT found
		existing           *cfclient.DNSRecord // nil = no matching A/CNAME record found
		adoptOptIn         bool                // cloudflare.io/adopt annotation present
		wantAction         RegistryAction
	}{
		// --- The six plan cases (§11.3) ---
		{
			// Case 1: no record, no TXT — create both
			name:               "no existing record and no TXT -> Create",
			cfg:                baseCfg,
			existingTXTContent: "",
			existing:           nil,
			adoptOptIn:         false,
			wantAction:         RegistryActionCreate,
		},
		{
			// Case 2: our TXT — reconcile normally
			name:               "existing record claimed by us -> Reconcile",
			cfg:                baseCfg,
			existingTXTContent: ownPayload(ownerID),
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionReconcile,
		},
		{
			// Case 3: import-allowed TXT — adopt
			name: "import-allowed TXT owner -> Adopt",
			cfg: RegistryConfig{
				TxtOwnerID:      ownerID,
				TxtImportOwners: []string{"external-dns-home"},
			},
			existingTXTContent: ownPayload("external-dns-home"),
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionAdopt,
		},
		{
			// Case 4: foreign TXT — refuse
			name:               "existing record with foreign TXT owner -> RefuseForeignOwner",
			cfg:                baseCfg,
			existingTXTContent: ownPayload("some-other-controller"),
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionRefuseForeignOwner,
		},
		{
			// Case 5: no TXT + record exists, no adopt flag — refuse
			name:               "existing record with NO TXT and adopt NOT opted in -> RefuseNoTXT",
			cfg:                baseCfg,
			existingTXTContent: "",
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionRefuseNoTXT,
		},
		{
			// Case 6: no TXT + record exists + adopt=true — adopt orphan
			name:               "existing record with NO TXT and adopt opted in -> AdoptOrphan",
			cfg:                baseCfg,
			existingTXTContent: "",
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         true,
			wantAction:         RegistryActionAdoptOrphan,
		},

		// --- Additional edge cases ---
		{
			// Decrypt-failure routes to RefuseForeignOwner (pattern §11.3)
			name: "decrypt failure -> RefuseForeignOwner",
			cfg: RegistryConfig{
				TxtOwnerID:           ownerID,
				TxtImportDecryptKeys: [][]byte{make([]byte, 32)}, // a real key
			},
			// Valid base64 of 32 bytes but NOT an AES-CBC-encrypted heritage payload.
			// DecryptPayload will attempt to decrypt (data >= 32 bytes, aligned to 16)
			// and fail with pkcs7 or sanity-check error → returns error → RefuseForeignOwner.
			existingTXTContent: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionRefuseForeignOwner,
		},
		{
			// Decode-failure routes to RefuseForeignOwner (pattern §11.3)
			name:               "decode failure (not heritage format) -> RefuseForeignOwner",
			cfg:                baseCfg,
			existingTXTContent: `"random-bytes-no-heritage"`,
			existing:           &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:         false,
			wantAction:         RegistryActionRefuseForeignOwner,
		},
		{
			// Owner comparison is case-sensitive
			name: "owner case mismatch does NOT match our owner -> RefuseForeignOwner",
			cfg:  baseCfg,
			existingTXTContent: cfclient.EncodeRegistryPayload(cfclient.RegistryPayload{
				Owner: "Cloudflare-Operator", // capitalised != ownerID
			}),
			existing:   &cfclient.DNSRecord{ID: "r3", Content: "1.2.3.4"},
			adoptOptIn: false,
			wantAction: RegistryActionRefuseForeignOwner,
		},
		{
			// Adopt opts in but no existing record — still Create (not AdoptOrphan)
			name:               "no TXT, adopt opted in, but no existing record -> Create not AdoptOrphan",
			cfg:                baseCfg,
			existingTXTContent: "",
			existing:           nil,
			adoptOptIn:         true,
			wantAction:         RegistryActionCreate,
		},
		{
			// Same-owner, same-resource -> Reconcile
			name:               "same owner same resource -> Reconcile",
			cfg:                baseCfg,
			existingTXTContent: ownPayload(ownerID),
			existing:           &cfclient.DNSRecord{ID: "r2", Content: "5.6.7.8"},
			adoptOptIn:         false,
			wantAction:         RegistryActionReconcile,
		},
		{
			// Foreign TXT with adopt opted in — adopt flag doesn't override foreign TXT
			name: "existing record with foreign TXT and adopt opted in -> still RefuseForeignOwner",
			cfg:  baseCfg,
			existingTXTContent: cfclient.EncodeRegistryPayload(cfclient.RegistryPayload{
				Owner: "external-dns",
			}),
			existing:   &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn: true,
			wantAction: RegistryActionRefuseForeignOwner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := RegistryDecide(tt.cfg, tt.existing, tt.existingTXTContent, tt.adoptOptIn)

			if action != tt.wantAction {
				t.Errorf("RegistryDecide() action = %v, want %v", action, tt.wantAction)
			}
		})
	}
}

// TestRegistrySentinels verifies that sentinel errors are defined and distinct.
func TestRegistrySentinels(t *testing.T) {
	if ErrForeignTXTOwner == nil {
		t.Fatal("ErrForeignTXTOwner must not be nil")
	}
	if ErrTXTRegistryGap == nil {
		t.Fatal("ErrTXTRegistryGap must not be nil")
	}
	if ErrSourceLabelsMissing == nil {
		t.Fatal("ErrSourceLabelsMissing must not be nil")
	}
	if errors.Is(ErrForeignTXTOwner, ErrTXTRegistryGap) {
		t.Error("ErrForeignTXTOwner and ErrTXTRegistryGap must be distinct")
	}
}
