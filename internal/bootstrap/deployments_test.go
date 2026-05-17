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

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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
	args := ApplyControllerSpec(v2alpha1.ControllerSpec{Enabled: true}, defaultImage)
	require.Equal(t, defaultImage, args.Image)
	require.Equal(t, int32(1), args.Replicas)
	require.Equal(t, "info", args.LogLevel)
}

func TestApplyControllerSpec_PreservesOverrides(t *testing.T) {
	args := ApplyControllerSpec(v2alpha1.ControllerSpec{
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
		TokenSecretRef: v2alpha1.SecretReference{Name: "cf-token", Key: "token"},
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

func TestBuildControllerDeployment_SecurityContext(t *testing.T) {
	dep := BuildControllerDeployment(BuildArgs{
		Bundle:    "zone",
		Namespace: "cf",
		Image:     "img:1",
		Replicas:  1,
	})
	podSC := dep.Spec.Template.Spec.SecurityContext
	require.NotNil(t, podSC, "pod security context must be set")
	require.NotNil(t, podSC.RunAsNonRoot)
	require.True(t, *podSC.RunAsNonRoot, "runAsNonRoot must be true")

	ctrSC := dep.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, ctrSC, "container security context must be set")
	require.NotNil(t, ctrSC.AllowPrivilegeEscalation)
	require.False(t, *ctrSC.AllowPrivilegeEscalation, "allowPrivilegeEscalation must be false")
	require.NotNil(t, ctrSC.Capabilities)
	require.Contains(t, ctrSC.Capabilities.Drop, corev1.Capability("ALL"))
}

func TestBuildControllerDeployment_LeaderElectionArg(t *testing.T) {
	enabled := BuildControllerDeployment(BuildArgs{
		Bundle:         "zone",
		Namespace:      "cf",
		Image:          "img:1",
		Replicas:       1,
		LeaderElection: true,
	})
	require.Contains(t, enabled.Spec.Template.Spec.Containers[0].Args, "--leader-election=true")

	disabled := BuildControllerDeployment(BuildArgs{
		Bundle:         "zone",
		Namespace:      "cf",
		Image:          "img:1",
		Replicas:       1,
		LeaderElection: false,
	})
	require.Contains(t, disabled.Spec.Template.Spec.Containers[0].Args, "--leader-election=false")
}
