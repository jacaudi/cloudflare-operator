package cloudflare

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
)

func TestIsPermissionDenied(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"403 cfgo.Error", &cfgo.Error{StatusCode: http.StatusForbidden}, true},
		{"401 cfgo.Error", &cfgo.Error{StatusCode: http.StatusUnauthorized}, false},
		{"404 cfgo.Error", &cfgo.Error{StatusCode: http.StatusNotFound}, false},
		{"500 cfgo.Error", &cfgo.Error{StatusCode: http.StatusInternalServerError}, false},
		{
			"wrapped 403",
			fmt.Errorf("update bot management: %w", &cfgo.Error{StatusCode: http.StatusForbidden}),
			true,
		},
		{
			"double-wrapped 403",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &cfgo.Error{StatusCode: http.StatusForbidden})),
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPermissionDenied(tc.err); got != tc.want {
				t.Errorf("IsPermissionDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
