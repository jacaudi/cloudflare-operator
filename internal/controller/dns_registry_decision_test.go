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

func ptr[T any](v T) *T { return &v }

func ownPayload(owner string) cfclient.RegistryPayload {
	return cfclient.RegistryPayload{
		Owner:           owner,
		SourceKind:      "httproute",
		SourceNamespace: "default",
		SourceName:      "my-route",
	}
}

// TestRegistryDecide covers all six verdicts plus additional edge cases
// called out in pattern #8.
func TestRegistryDecide(t *testing.T) {
	const ownerID = "cloudflare-operator"

	tests := []struct {
		name        string
		existingTXT *cfclient.RegistryPayload // nil = no companion TXT found
		existing    *cfclient.DNSRecord       // nil = no matching A/CNAME record found
		adoptOptIn  bool                      // cloudflare.io/adopt annotation present
		wantAction  RegistryAction
		wantErrIs   error // nil = no error expected
	}{
		// --- The six base verdicts ---
		{
			name:        "no existing record and no TXT -> Create",
			existingTXT: nil,
			existing:    nil,
			adoptOptIn:  false,
			wantAction:  RegistryActionCreate,
		},
		{
			name:        "existing record claimed by us -> Reconcile",
			existingTXT: ptr(ownPayload(ownerID)),
			existing:    &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:  false,
			wantAction:  RegistryActionReconcile,
		},
		{
			name: "existing record with foreign TXT owner -> RefuseForeignOwner",
			existingTXT: ptr(cfclient.RegistryPayload{
				Owner: "external-dns",
			}),
			existing:   &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn: false,
			wantAction: RegistryActionRefuseForeignOwner,
			wantErrIs:  ErrForeignTXTOwner,
		},
		{
			name:        "existing record with NO TXT and adopt NOT opted in -> RefuseOrphan",
			existingTXT: nil,
			existing:    &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:  false,
			wantAction:  RegistryActionRefuseOrphan,
			wantErrIs:   ErrTXTRegistryGap,
		},
		{
			name:        "existing record with foreign TXT and adopt opted in -> still RefuseForeignOwner",
			existingTXT: ptr(cfclient.RegistryPayload{Owner: "external-dns"}),
			existing:    &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:  true,
			wantAction:  RegistryActionRefuseForeignOwner,
			wantErrIs:   ErrForeignTXTOwner,
		},
		{
			name:        "existing record with NO TXT and adopt opted in -> AdoptOrphan",
			existingTXT: nil,
			existing:    &cfclient.DNSRecord{ID: "r1", Content: "1.2.3.4"},
			adoptOptIn:  true,
			wantAction:  RegistryActionAdoptOrphan,
		},
		{
			name:        "existing TXT claimed by us but NO DNS record -> Adopt (recreate)",
			existingTXT: ptr(ownPayload(ownerID)),
			existing:    nil,
			adoptOptIn:  false,
			wantAction:  RegistryActionAdopt,
		},

		// --- Additional edge cases (pattern #8) ---
		{
			// Empty TXT slice + adoptOptIn=true + no existing record => Create (not AdoptOrphan)
			name:        "no TXT, adopt opted in, but no existing record -> Create not AdoptOrphan",
			existingTXT: nil,
			existing:    nil,
			adoptOptIn:  true,
			wantAction:  RegistryActionCreate,
		},
		{
			// Same-owner record and the resource labels exactly match -> Reconcile
			name:        "same owner same resource -> Reconcile",
			existingTXT: ptr(ownPayload(ownerID)),
			existing:    &cfclient.DNSRecord{ID: "r2", Content: "5.6.7.8"},
			adoptOptIn:  false,
			wantAction:  RegistryActionReconcile,
		},
		{
			// Owner comparison is case-sensitive: "External-DNS-Home" != "external-dns"
			name: "owner case mismatch does NOT match our owner -> RefuseForeignOwner",
			existingTXT: ptr(cfclient.RegistryPayload{
				Owner: "Cloudflare-Operator", // capitalised != ownerID
			}),
			existing:   &cfclient.DNSRecord{ID: "r3", Content: "1.2.3.4"},
			adoptOptIn: false,
			wantAction: RegistryActionRefuseForeignOwner,
			wantErrIs:  ErrForeignTXTOwner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := RegistryDecide(ownerID, tt.existingTXT, tt.existing, tt.adoptOptIn)

			if action != tt.wantAction {
				t.Errorf("RegistryDecide() action = %v, want %v", action, tt.wantAction)
			}

			if tt.wantErrIs != nil {
				if err == nil {
					t.Fatalf("expected error wrapping %v, got nil", tt.wantErrIs)
				}
				if !errors.Is(err, tt.wantErrIs) {
					t.Errorf("expected errors.Is(%v), got %v", tt.wantErrIs, err)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
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

// TestRegistryAction_String checks that the RegistryAction type has legible
// String values (avoids numeric surprises in log output).
func TestRegistryAction_String(t *testing.T) {
	actions := []RegistryAction{
		RegistryActionCreate,
		RegistryActionReconcile,
		RegistryActionAdopt,
		RegistryActionAdoptOrphan,
		RegistryActionRefuseForeignOwner,
		RegistryActionRefuseOrphan,
	}
	seen := map[string]RegistryAction{}
	for _, a := range actions {
		s := a.String()
		if s == "" {
			t.Errorf("RegistryAction(%d).String() returned empty string", a)
		}
		if prev, ok := seen[s]; ok {
			t.Errorf("duplicate String() %q for actions %v and %v", s, prev, a)
		}
		seen[s] = a
	}
}
