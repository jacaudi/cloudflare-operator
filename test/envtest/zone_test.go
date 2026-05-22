/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/zone"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
)

// TestZoneBundle_EnvtestAcceptance covers spec 2 §10 acceptance criteria for
// the zone bundle end-to-end against the envtest API server with mock-backed
// Cloudflare clients. Each sub-test maps to one §10 item.
func TestZoneBundle_EnvtestAcceptance(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	// LoadCredentialsHierarchical falls back to env-var creds when the CR has
	// no override — every zone-bundle reconciler calls it on each Reconcile.
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mock.New()

	// Wire each zone-bundle reconciler with a mock-backed *Fn factory. We do
	// the wiring inline (rather than calling zone.AddToManager) so the test
	// can inject the same mock instance across all four controllers.
	zoneR := &zone.CloudflareZoneReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) {
			return m.Zone, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.CloudflareZone{}).
		Complete(zoneR))

	zcR := &zone.CloudflareZoneConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) {
			return m.ZoneConfig, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.CloudflareZoneConfig{}).
		Complete(zcR))

	dnsR := &zone.CloudflareDNSRecordReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) {
			return m.DNS, nil
		},
		IPResolver: ipresolver.NewResolver(),
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.CloudflareDNSRecord{}).
		Complete(dnsR))

	rsR := &zone.CloudflareRulesetReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) {
			return m.Ruleset, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.CloudflareRuleset{}).
		Complete(rsR))

	ctx := t.Context()
	go func() { _ = mgr.Start(ctx) }()

	// Block until the manager's informer cache is populated; mgr.GetClient()
	// returns a cached reader and `the cache is not started` errors until
	// caches sync.
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	c := mgr.GetClient()

	// zoneID is captured by §10.2 from Status.ZoneID and reused by the
	// downstream sub-tests (§10.3/§10.4/§10.5) to decouple them from the
	// mock's internal ID-generation scheme.
	var zoneID string

	t.Run("§10.1 CRDs install + sample CRs listable", func(t *testing.T) {
		var zl v2alpha1.CloudflareZoneList
		require.NoError(t, c.List(ctx, &zl))
		var dl v2alpha1.CloudflareDNSRecordList
		require.NoError(t, c.List(ctx, &dl))
		var zcl v2alpha1.CloudflareZoneConfigList
		require.NoError(t, c.List(ctx, &zcl))
		var rl v2alpha1.CloudflareRulesetList
		require.NoError(t, c.List(ctx, &rl))
	})

	t.Run("§10.2 Zone create populates Status.ZoneID", func(t *testing.T) {
		z := &v2alpha1.CloudflareZone{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
			Spec: v2alpha1.CloudflareZoneSpec{
				Name:           "example.com",
				Type:           "full",
				DeletionPolicy: v2alpha1.DeletionPolicyRetain,
			},
		}
		require.NoError(t, c.Create(ctx, z))
		require.Eventually(t, func() bool {
			var got v2alpha1.CloudflareZone
			if err := c.Get(ctx, types.NamespacedName{Name: "example", Namespace: "default"}, &got); err != nil {
				return false
			}
			if got.Status.ZoneID == "" {
				return false
			}
			zoneID = got.Status.ZoneID
			return true
		}, 10*time.Second, 200*time.Millisecond, "Status.ZoneID populated")
	})

	t.Run("§10.3 ZoneConfig group condition", func(t *testing.T) {
		require.NotEmpty(t, zoneID, "§10.2 must populate zoneID before downstream tests")
		mode := "strict"
		cfg := &v2alpha1.CloudflareZoneConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Spec: v2alpha1.CloudflareZoneConfigSpec{
				ZoneID: zoneID,
				SSL:    &v2alpha1.SSLSettings{Mode: &mode},
			},
		}
		require.NoError(t, c.Create(ctx, cfg))
		require.Eventually(t, func() bool {
			var got v2alpha1.CloudflareZoneConfig
			if err := c.Get(ctx, types.NamespacedName{Name: "cfg", Namespace: "default"}, &got); err != nil {
				return false
			}
			for _, cd := range got.Status.Conditions {
				if cd.Type == conventions.ConditionTypeSSLApplied && cd.Status == metav1.ConditionTrue {
					return true
				}
			}
			return false
		}, 10*time.Second, 200*time.Millisecond, "SSLApplied=True")
	})

	t.Run("§10.4 DNSRecord adopt by TXT-verified (name, type) match", func(t *testing.T) {
		require.NotEmpty(t, zoneID, "§10.2 must populate zoneID before downstream tests")
		// Seed mock with a pre-existing A record at the same (name, type) so the
		// TXT-verified adopt path can take it over (rather than falling through to
		// Create). P5 requires a matching TXT companion to prove ownership before
		// adoption succeeds; bare (name, type) matching no longer suffices.
		_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
			Name: "app.example.com", Type: "A", Content: "192.0.2.10", TTL: 1,
		})
		require.NoError(t, err)
		// Seed matching TXT companion that encodes ownership of rec-adopt/default.
		txtName := cloudflare.AffixName("cf-txt", "app.example.com")
		payload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "rec-adopt"}
		plainContent, encErr := cloudflare.NewPlaintextCodec().Encode(payload)
		require.NoError(t, encErr)
		_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
			Type: "TXT", Name: txtName, Content: plainContent, TTL: 1,
		})
		require.NoError(t, err)
		content := "192.0.2.20"
		rec := &v2alpha1.CloudflareDNSRecord{
			ObjectMeta: metav1.ObjectMeta{Name: "rec-adopt", Namespace: "default"},
			Spec: v2alpha1.CloudflareDNSRecordSpec{
				Name:    "app.example.com",
				Type:    "A",
				Content: &content,
				ZoneID:  zoneID,
				Adopt:   true,
			},
		}
		require.NoError(t, c.Create(ctx, rec))
		require.Eventually(t, func() bool {
			var got v2alpha1.CloudflareDNSRecord
			if err := c.Get(ctx, types.NamespacedName{Name: "rec-adopt", Namespace: "default"}, &got); err != nil {
				return false
			}
			// TXT-verified adoption + drift correction: RecordID and TxtRecordID
			// both populated, and CurrentContent matches the spec content.
			return got.Status.RecordID != "" && got.Status.TxtRecordID != "" && got.Status.CurrentContent == "192.0.2.20"
		}, 10*time.Second, 200*time.Millisecond, "adopted via TXT-verified ownership + drift corrected")
	})

	t.Run("§10.5 Ruleset PUT-entrypoint creates rules", func(t *testing.T) {
		require.NotEmpty(t, zoneID, "§10.2 must populate zoneID before downstream tests")
		rs := &v2alpha1.CloudflareRuleset{
			ObjectMeta: metav1.ObjectMeta{Name: "waf", Namespace: "default"},
			Spec: v2alpha1.CloudflareRulesetSpec{
				ZoneID: zoneID,
				Name:   "waf",
				Phase:  "http_request_firewall_custom",
				Rules: []v2alpha1.RulesetRuleSpec{{
					Action:     "block",
					Expression: `(ip.src eq 192.0.2.4)`,
				}},
			},
		}
		require.NoError(t, c.Create(ctx, rs))
		require.Eventually(t, func() bool {
			got, err := m.Ruleset.GetPhaseEntrypoint(ctx, zoneID, "http_request_firewall_custom")
			return err == nil && got != nil && len(got.Rules) == 1
		}, 10*time.Second, 200*time.Millisecond, "ruleset entrypoint created with 1 rule")
	})
}
