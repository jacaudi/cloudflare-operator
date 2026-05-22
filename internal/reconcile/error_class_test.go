/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestErrorClass covers each named class + the unknown fallback.
func TestErrorClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"name_miss", ErrNameMiss, "name-miss"},
		{"foreign", ErrForeign, "foreign"},
		{"undecodable", ErrUndecodable, "undecodable"},
		{"cf_api_81058_identical_record", errors.New("Cloudflare API error: code 81058: An identical record already exists"), "cf-api-81058"},
		{"cf_api_81053_record_exists", errors.New("400 code 81053 A/AAAA/CNAME with that host already exists"), "cf-api-81053"},
		{"cf_api_9207_other", errors.New("Cloudflare API: code 9207: Content for a record of type TXT is invalid."), "cf-api-9207"},
		{"unknown_error", errors.New("some unrelated error"), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ErrorClass(tc.err)
			require.Equal(t, tc.want, got, "err=%v", tc.err)
		})
	}
}

// TestErrorClass_WrappedSentinels: errors.Is unwrapping must hold so callers
// can `fmt.Errorf("context: %w", ErrForeign)` and still get the right class.
func TestErrorClass_WrappedSentinels(t *testing.T) {
	wrapped := errors.New("ownership verify failed: " + ErrForeign.Error())
	// SENTINEL CLASSIFICATION REQUIRES errors.Is — substring-only matches
	// are intentionally NOT classified. Plain-string wraps that happen to
	// contain a sentinel's text fall through to "unknown" to prevent false
	// positives.
	properly := errFmtWrap(ErrForeign)
	require.Equal(t, "foreign", ErrorClass(properly))
	// A plain-string wrap that mentions the sentinel's text is NOT
	// classified as `foreign` — only true sentinel-wrapped errors are.
	require.Equal(t, "unknown", ErrorClass(wrapped))
}

// errFmtWrap wraps target so that errors.Is(returned, target) == true,
// simulating a caller doing fmt.Errorf("context: %w", target). Note:
// errors.Join produces a multi-error (Unwrap() []error), whereas
// fmt.Errorf("%w") produces a single-parent chain — both satisfy errors.Is
// but differ for errors.As traversal. Sufficient for testing ErrorClass,
// which only uses errors.Is internally.
func errFmtWrap(target error) error {
	return errors.Join(errors.New("outer context"), target)
}
