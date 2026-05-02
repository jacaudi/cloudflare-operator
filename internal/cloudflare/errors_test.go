package cloudflare

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"
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

func TestIsBadRequest(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"400 cfgo.Error", &cfgo.Error{StatusCode: http.StatusBadRequest}, true},
		{"403 cfgo.Error", &cfgo.Error{StatusCode: http.StatusForbidden}, false},
		{"404 cfgo.Error", &cfgo.Error{StatusCode: http.StatusNotFound}, false},
		{"500 cfgo.Error", &cfgo.Error{StatusCode: http.StatusInternalServerError}, false},
		{
			"wrapped 400",
			fmt.Errorf("create record: %w", &cfgo.Error{StatusCode: http.StatusBadRequest}),
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBadRequest(tc.err); got != tc.want {
				t.Errorf("IsBadRequest(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"404 cfgo.Error", &cfgo.Error{StatusCode: http.StatusNotFound}, true},
		{"400 cfgo.Error", &cfgo.Error{StatusCode: http.StatusBadRequest}, false},
		{"403 cfgo.Error", &cfgo.Error{StatusCode: http.StatusForbidden}, false},
		{"500 cfgo.Error", &cfgo.Error{StatusCode: http.StatusInternalServerError}, false},
		{
			"wrapped 404",
			fmt.Errorf("get record: %w", &cfgo.Error{StatusCode: http.StatusNotFound}),
			true,
		},
		{
			"double-wrapped 404",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &cfgo.Error{StatusCode: http.StatusNotFound})),
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsPlanTierRequired(t *testing.T) {
	planTierErr := &cfgo.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 1015, Message: "feature requires a higher plan"}},
	}
	wrongCodeErr := &cfgo.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 9000}},
	}
	multiCodeIncludingPlanTier := &cfgo.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 9999}, {Code: 1015}},
	}
	planTierWrongStatus := &cfgo.Error{
		StatusCode: http.StatusNotFound,
		Errors:     []shared.ErrorData{{Code: 1015}},
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"403 with code 1015", planTierErr, true},
		{"403 with code 9000", wrongCodeErr, false},
		{"403 with empty Errors slice", &cfgo.Error{StatusCode: http.StatusForbidden}, false},
		{"403 with mixed codes including 1015", multiCodeIncludingPlanTier, true},
		{"404 with code 1015", planTierWrongStatus, false},
		{
			"wrapped 403/1015",
			fmt.Errorf("update bot management: %w", planTierErr),
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPlanTierRequired(tc.err); got != tc.want {
				t.Errorf("IsPlanTierRequired(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
