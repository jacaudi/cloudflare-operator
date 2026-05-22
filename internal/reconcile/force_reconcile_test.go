/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestForceReconcileRequested covers the full truth table:
//
//	annotationToken  lastAck   result   reason
//	---------------  --------  -------  -----------------------------------------
//	""               <any>     false    no annotation: no force requested
//	"<token>"        ""        true     never-acked: forced (covers freshly-set)
//	"<token>"        "<same>"  false    already-acked: no force
//	"<token>"        "<diff>"  true     token changed: forced
func TestForceReconcileRequested(t *testing.T) {
	cases := []struct {
		name            string
		annotationToken string
		lastAck         string
		want            bool
	}{
		{"annotation_absent", "", "", false},
		{"annotation_absent_with_old_ack", "", "abc", false},
		{"new_token_never_acked", "t1", "", true},
		{"new_token_first_set", "2026-05-20T12:00:00Z", "", true},
		{"token_unchanged", "t1", "t1", false},
		{"token_changed", "t2", "t1", true},
		{"identical_long_tokens", "uuid-12345-67890", "uuid-12345-67890", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ForceReconcileRequested(tc.annotationToken, tc.lastAck)
			require.Equal(t, tc.want, got,
				"annotationToken=%q lastAck=%q", tc.annotationToken, tc.lastAck)
		})
	}
}
