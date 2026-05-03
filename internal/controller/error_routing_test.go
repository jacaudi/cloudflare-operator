package controller

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func TestClassifyCloudflareError(t *testing.T) {
	planTierErr := &cfgo.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 1015}},
	}

	cases := []struct {
		name            string
		err             error
		wantReason      string
		wantRequeue     time.Duration
		wantResetRemote bool
	}{
		{
			"nil err returns zero ErrorRouting",
			nil,
			"",
			0,
			false,
		},
		{
			"404 routes to RemoteGone with immediate requeue and ResetRemoteID",
			&cfgo.Error{StatusCode: http.StatusNotFound},
			cloudflarev1alpha1.ReasonRemoteGone,
			0,
			true,
		},
		{
			"400 routes to InvalidSpec with 1h requeue",
			&cfgo.Error{StatusCode: http.StatusBadRequest},
			cloudflarev1alpha1.ReasonInvalidSpec,
			time.Hour,
			false,
		},
		{
			"403 with code 1015 routes to PlanTierRequired before PermissionDenied",
			planTierErr,
			cloudflarev1alpha1.ReasonPlanTierRequired,
			time.Hour,
			false,
		},
		{
			"403 without plan-tier code routes to PermissionDenied",
			&cfgo.Error{StatusCode: http.StatusForbidden},
			cloudflarev1alpha1.ReasonPermissionDenied,
			time.Hour,
			false,
		},
		{
			"500 falls through to catch-all (CloudflareAPIError, zero requeue)",
			&cfgo.Error{StatusCode: http.StatusInternalServerError},
			cloudflarev1alpha1.ReasonCloudflareError,
			0,
			false,
		},
		{
			"plain non-API error falls through to catch-all",
			errors.New("network blip"),
			cloudflarev1alpha1.ReasonCloudflareError,
			0,
			false,
		},
		{
			"wrapped 404 still routes to RemoteGone",
			fmt.Errorf("get record by ID: %w", &cfgo.Error{StatusCode: http.StatusNotFound}),
			cloudflarev1alpha1.ReasonRemoteGone,
			0,
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCloudflareError(tc.err)
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.RequeueAfter != tc.wantRequeue {
				t.Errorf("RequeueAfter = %v, want %v", got.RequeueAfter, tc.wantRequeue)
			}
			if got.ResetRemoteID != tc.wantResetRemote {
				t.Errorf("ResetRemoteID = %v, want %v", got.ResetRemoteID, tc.wantResetRemote)
			}
		})
	}
}
