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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestZoneConfig_AllSixGroupsApply(t *testing.T) {
	mode := "strict"
	level := "high"
	cache := "aggressive"
	ipv6 := "on"
	cname := "flatten_at_root"
	enableJS := true
	cfg := &v1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: v1alpha1.CloudflareZoneConfigSpec{
			ZoneID:        "z1",
			SSL:           &v1alpha1.SSLSettings{Mode: &mode},
			Security:      &v1alpha1.SecuritySettings{SecurityLevel: &level},
			Performance:   &v1alpha1.PerformanceSettings{CacheLevel: &cache},
			Network:       &v1alpha1.NetworkSettings{IPv6: &ipv6},
			DNS:           &v1alpha1.DNSSettings{CNAMEFlattening: &cname},
			BotManagement: &v1alpha1.BotManagementSettings{EnableJS: &enableJS},
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cfg).WithStatusSubresource(&v1alpha1.CloudflareZoneConfig{}).Build()
	m := mock.New()
	r := &CloudflareZoneConfigReconciler{
		Client: c, Scheme: s,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) { return m.ZoneConfig, nil },
	}
	// First reconcile may set the finalizer + requeue; do up to 2 passes to converge.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareZoneConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg", Namespace: "default"}, &got))

	conds := condMap(got.Status.Conditions)
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeSSLApplied])
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeSecurityApplied])
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypePerformanceApplied])
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeNetworkApplied])
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeDNSApplied])
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeBotManagementApplied])
	require.NotEmpty(t, got.Status.AppliedSpecHash)
	require.Equal(t, v1alpha1.PhaseReady, got.Status.Phase)
	require.Len(t, conds, 7, "six groups + Ready")
}

func TestZoneConfig_FastSkipOnUnchangedHash(t *testing.T) {
	mode := "strict"
	cfg := &v1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec:       v1alpha1.CloudflareZoneConfigSpec{ZoneID: "z1", SSL: &v1alpha1.SSLSettings{Mode: &mode}},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cfg).WithStatusSubresource(&v1alpha1.CloudflareZoneConfig{}).Build()
	m := mock.New()
	r := &CloudflareZoneConfigReconciler{Client: c, Scheme: s,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) { return m.ZoneConfig, nil },
	}
	// Converge: up to 2 reconciles to set finalizer + apply.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	// Now hash should be set; the next reconcile should fast-skip without
	// hitting UpdateSetting. Inject an error to detect any API call.
	calls := 0
	m.InjectError("ZoneConfig.UpdateSetting", &countingErr{calls: &calls})
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	require.Zero(t, calls, "fast-skip skips API calls on unchanged hash")
}

func TestZoneConfig_FastSkipSkipsClientConstruction(t *testing.T) {
	mode := "strict"
	cfg := &v1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec:       v1alpha1.CloudflareZoneConfigSpec{ZoneID: "z1", SSL: &v1alpha1.SSLSettings{Mode: &mode}},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cfg).WithStatusSubresource(&v1alpha1.CloudflareZoneConfig{}).Build()

	// Converge to Ready state so AppliedSpecHash is set.
	m := mock.New()
	r := &CloudflareZoneConfigReconciler{Client: c, Scheme: s,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) { return m.ZoneConfig, nil },
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	// Now swap ZoneConfigClientFn to one that errors. Fast-skip must not call it.
	r.ZoneConfigClientFn = func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) {
		return nil, errors.New("client construction should not happen on fast-skip")
	}
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err, "fast-skip must not call ZoneConfigClientFn")
}

func condMap(cs []metav1.Condition) map[string]metav1.ConditionStatus {
	out := map[string]metav1.ConditionStatus{}
	for _, c := range cs {
		out[c.Type] = c.Status
	}
	return out
}

// condReason returns the Reason string for the named condition type, or "" if absent.
func condReason(cs []metav1.Condition, condType string) string {
	for _, c := range cs {
		if c.Type == condType {
			return c.Reason
		}
	}
	return ""
}

type countingErr struct{ calls *int }

func (e *countingErr) Error() string { *e.calls++; return "boom" }

// TestApplyAllGroups_FansOutConcurrently asserts that the 6 setting groups
// apply concurrently. With a fake client that sleeps perCallDelay on every
// UpdateSetting / UpdateBotManagement call, the total elapsed time should be
// ~1× perCallDelay, not 6×. The LessOrEqual bound of 3× provides generous
// headroom for scheduler jitter while still catching accidental serial execution.
func TestApplyAllGroups_FansOutConcurrently(t *testing.T) {
	const perCallDelay = 50 * time.Millisecond

	// One non-nil field per group so each applyXGroup actually invokes the
	// Cloudflare client rather than hitting the nil-spec fast-skip path.
	sslMode := "strict"
	secLevel := "high"
	perfCache := "aggressive"
	netIPv6 := "on"
	dnsCNAME := "flatten_at_root"
	botEnableJS := true

	cfg := &v1alpha1.CloudflareZoneConfig{
		Spec: v1alpha1.CloudflareZoneConfigSpec{
			SSL:           &v1alpha1.SSLSettings{Mode: &sslMode},
			Security:      &v1alpha1.SecuritySettings{SecurityLevel: &secLevel},
			Performance:   &v1alpha1.PerformanceSettings{CacheLevel: &perfCache},
			Network:       &v1alpha1.NetworkSettings{IPv6: &netIPv6},
			DNS:           &v1alpha1.DNSSettings{CNAMEFlattening: &dnsCNAME},
			BotManagement: &v1alpha1.BotManagementSettings{EnableJS: &botEnableJS},
		},
	}

	blocker := &blockingZoneConfigClient{delay: perCallDelay}

	start := time.Now()
	results := applyAllGroups(context.Background(), blocker, "zone-id", cfg)
	elapsed := time.Since(start)

	require.Len(t, results, 6, "expected 6 group results")
	for _, r := range results {
		require.False(t, r.skip, "no group should be skipped")
		require.NoError(t, r.err, "no group should error")
	}
	require.LessOrEqual(t, elapsed, 3*perCallDelay,
		"expected fan-out (≤ ~150ms); got %v — likely still serial", elapsed)
	require.GreaterOrEqual(t, elapsed, perCallDelay,
		"expected at least one full call delay; got %v", elapsed)
}

// blockingZoneConfigClient is a stub implementing cloudflare.ZoneConfigClient
// that sleeps `delay` on every method to simulate per-call latency. This lets
// the fan-out timing test verify that the 6 groups run concurrently.
type blockingZoneConfigClient struct {
	delay time.Duration
}

func (b *blockingZoneConfigClient) UpdateSetting(ctx context.Context, zoneID, settingID string, value any) error {
	select {
	case <-time.After(b.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingZoneConfigClient) GetBotManagement(ctx context.Context, zoneID string) (*cloudflare.BotManagementConfig, error) {
	select {
	case <-time.After(b.delay):
		return &cloudflare.BotManagementConfig{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingZoneConfigClient) UpdateBotManagement(ctx context.Context, zoneID string, config cloudflare.BotManagementConfig) error {
	select {
	case <-time.After(b.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
