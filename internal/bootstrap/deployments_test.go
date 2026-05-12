package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func TestBuildControllerDeployment_ZoneMode(t *testing.T) {
	dep := BuildControllerDeployment(BuildArgs{
		Bundle:         "zone",
		Namespace:      "cloudflare-system",
		Image:          "ghcr.io/foo/manager:1.0",
		Replicas:       2,
		LogLevel:       "info",
		MetricsAddress: ":8080",
		HealthAddress:  ":8081",
	})
	require.Equal(t, "cloudflare-zone-controller", dep.Name)
	require.Equal(t, "cloudflare-system", dep.Namespace)
	require.Equal(t, int32(2), *dep.Spec.Replicas)
	require.Equal(t, "ghcr.io/foo/manager:1.0", dep.Spec.Template.Spec.Containers[0].Image)
	require.Contains(t, dep.Spec.Template.Spec.Containers[0].Args, "--mode=zone")
	require.Contains(t, dep.Spec.Template.Spec.Containers[0].Args, "--log-level=info")
}

func TestBuildControllerDeployment_TunnelMode(t *testing.T) {
	dep := BuildControllerDeployment(BuildArgs{
		Bundle:    "tunnel",
		Namespace: "cf",
		Image:     "img:t",
		Replicas:  1,
	})
	require.Equal(t, "cloudflare-tunnel-controller", dep.Name)
	require.Contains(t, dep.Spec.Template.Spec.Containers[0].Args, "--mode=tunnel")
}

func TestApplyControllerSpec_FillsDefaults(t *testing.T) {
	defaultImage := "default:1.0"
	args := ApplyControllerSpec(v1alpha1.ControllerSpec{Enabled: true}, defaultImage)
	require.Equal(t, defaultImage, args.Image)
	require.Equal(t, int32(1), args.Replicas)
	require.Equal(t, "info", args.LogLevel)
}

func TestApplyControllerSpec_PreservesOverrides(t *testing.T) {
	args := ApplyControllerSpec(v1alpha1.ControllerSpec{
		Enabled:  true,
		Image:    "custom:2",
		Replicas: 3,
		LogLevel: "debug",
	}, "default:1")
	require.Equal(t, "custom:2", args.Image)
	require.Equal(t, int32(3), args.Replicas)
	require.Equal(t, "debug", args.LogLevel)
}

func TestBuildControllerDeployment_EnvCredentialPassthrough(t *testing.T) {
	dep := BuildControllerDeployment(BuildArgs{
		Bundle:         "zone",
		Namespace:      "cf",
		Image:          "img:1",
		Replicas:       1,
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Key: "token"},
		AccountID:      "acct-123",
	})
	env := dep.Spec.Template.Spec.Containers[0].Env
	var sawAccount, sawToken bool
	for _, e := range env {
		if e.Name == "CLOUDFLARE_ACCOUNT_ID" {
			require.Equal(t, "acct-123", e.Value)
			sawAccount = true
		}
		if e.Name == "CLOUDFLARE_API_TOKEN" {
			require.NotNil(t, e.ValueFrom)
			require.NotNil(t, e.ValueFrom.SecretKeyRef)
			require.Equal(t, "cf-token", e.ValueFrom.SecretKeyRef.Name)
			require.Equal(t, "token", e.ValueFrom.SecretKeyRef.Key)
			sawToken = true
		}
	}
	require.True(t, sawAccount, "expected CLOUDFLARE_ACCOUNT_ID env var")
	require.True(t, sawToken, "expected CLOUDFLARE_API_TOKEN env var sourced from secretKeyRef")
}
