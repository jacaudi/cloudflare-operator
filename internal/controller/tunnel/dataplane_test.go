/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

const testDefaultImage = "docker.io/cloudflare/cloudflared:2026.4.1"

func mkTunnel(name, ns string, mut func(*v2alpha1.CloudflareTunnel)) *v2alpha1.CloudflareTunnel {
	tn := &v2alpha1.CloudflareTunnel{
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: name,
			Connector: v2alpha1.ConnectorSpec{
				Replicas:           2,
				Protocol:           "auto",
				LogLevel:           "info",
				GracePeriodSeconds: 30,
			},
		},
	}
	tn.Name = name
	tn.Namespace = ns
	if mut != nil {
		mut(tn)
	}
	return tn
}

func TestBuildDeployment_NamingTemplate(t *testing.T) {
	tn := mkTunnel("app-foo-payments", "app-foo", nil)
	dep := BuildDeployment(tn, testDefaultImage)
	require.Equal(t, "cloudflared-app-foo-payments", dep.Name)
	require.Equal(t, "app-foo", dep.Namespace)
}

func TestBuildDeployment_GracePeriodPlus15(t *testing.T) {
	tn := mkTunnel("t", "ns", func(c *v2alpha1.CloudflareTunnel) { c.Spec.Connector.GracePeriodSeconds = 30 })
	dep := BuildDeployment(tn, testDefaultImage)
	require.NotNil(t, dep.Spec.Template.Spec.TerminationGracePeriodSeconds)
	require.Equal(t, int64(45), *dep.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func TestBuildDeployment_RequiredArgs(t *testing.T) {
	tn := mkTunnel("t", "ns", nil)
	dep := BuildDeployment(tn, testDefaultImage)
	args := dep.Spec.Template.Spec.Containers[0].Args
	require.Contains(t, args, "tunnel")
	require.Contains(t, args, "--no-autoupdate")
	require.Contains(t, args, "--metrics=0.0.0.0:2000")
	require.Contains(t, args, "--protocol=auto")
	require.Contains(t, args, "--grace-period=30s")
	require.Contains(t, args, "--loglevel=info")
	require.Contains(t, args, "run")
}

func TestBuildDeployment_TokenEnvFromSecret(t *testing.T) {
	tn := mkTunnel("t", "ns", nil)
	dep := BuildDeployment(tn, testDefaultImage)
	c := dep.Spec.Template.Spec.Containers[0]
	require.NotEmpty(t, c.Env)
	require.Equal(t, "TUNNEL_TOKEN", c.Env[0].Name)
	require.NotNil(t, c.Env[0].ValueFrom)
	require.NotNil(t, c.Env[0].ValueFrom.SecretKeyRef)
	require.Equal(t, "cloudflared-token-t", c.Env[0].ValueFrom.SecretKeyRef.Name)
}

func TestBuildDeployment_ReadinessProbe(t *testing.T) {
	tn := mkTunnel("t", "ns", nil)
	dep := BuildDeployment(tn, testDefaultImage)
	probe := dep.Spec.Template.Spec.Containers[0].ReadinessProbe
	require.NotNil(t, probe)
	require.NotNil(t, probe.HTTPGet)
	require.Equal(t, "/ready", probe.HTTPGet.Path)
}

func TestBuildDeployment_OriginCAVolumeMounted(t *testing.T) {
	tn := mkTunnel("t", "ns", func(c *v2alpha1.CloudflareTunnel) {
		c.Spec.Connector.OriginCASecretRef = &v2alpha1.SecretReference{Name: "ca", Key: "bundle.crt"}
	})
	dep := BuildDeployment(tn, testDefaultImage)
	vols := dep.Spec.Template.Spec.Volumes
	require.Len(t, vols, 1)
	require.Equal(t, "origin-ca", vols[0].Name)
	require.NotNil(t, vols[0].Secret)
	require.Equal(t, "ca", vols[0].Secret.SecretName)
	mounts := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	require.Len(t, mounts, 1)
	require.Equal(t, "/etc/cloudflared/ca", mounts[0].MountPath)
}

func TestBuildDeployment_ResourceDefaults_Independent(t *testing.T) {
	// Half-set: Requests only — Limits should still get safety floor (pattern #10).
	tn := mkTunnel("t", "ns", func(c *v2alpha1.CloudflareTunnel) {
		c.Spec.Connector.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		}
	})
	dep := BuildDeployment(tn, testDefaultImage)
	res := dep.Spec.Template.Spec.Containers[0].Resources
	require.Equal(t, "200m", res.Requests.Cpu().String(), "user CPU request preserved")
	require.NotEmpty(t, res.Limits, "Limits default applied independently")
}

func TestBuildDeployment_ResourceDefaults_LimitsOnly(t *testing.T) {
	// Symmetric to TestBuildDeployment_ResourceDefaults_Independent: user sets
	// only Limits, default Requests must apply independently (pattern #10).
	tn := mkTunnel("t", "ns", func(c *v2alpha1.CloudflareTunnel) {
		c.Spec.Connector.Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		}
	})
	dep := BuildDeployment(tn, testDefaultImage)
	res := dep.Spec.Template.Spec.Containers[0].Resources
	require.Equal(t, "512Mi", res.Limits.Memory().String(), "user Memory limit preserved")
	require.NotEmpty(t, res.Requests, "Requests default applied independently")
	require.Equal(t, "50m", res.Requests.Cpu().String(), "default Requests CPU applied")
}

func TestBuildDeployment_ImageDefault_HalfOverride(t *testing.T) {
	// Repository-only override; Tag from default (pattern #9).
	tn := mkTunnel("t", "ns", func(c *v2alpha1.CloudflareTunnel) {
		c.Spec.Connector.Image = &v2alpha1.ConnectorImage{Repository: "private.example.com/cloudflared"}
	})
	dep := BuildDeployment(tn, testDefaultImage)
	img := dep.Spec.Template.Spec.Containers[0].Image
	require.Contains(t, img, "private.example.com/cloudflared:")
	require.Contains(t, img, ":2026.4.1")
}

// TestBuildDeployment_SecurityContext verifies the pod-template-spec and
// container-level security contexts match the Foundation hardening profile:
// runAsNonRoot, readOnlyRootFilesystem, capabilities.drop=ALL, seccomp=
// RuntimeDefault. Mirrors internal/bootstrap/deployments.go::BuildController-
// Deployment so cloudflared Pods carry the same baseline as the operator's
// own Pods.
func TestBuildDeployment_SecurityContext(t *testing.T) {
	tn := mkTunnel("t", "ns", nil)
	dep := BuildDeployment(tn, testDefaultImage)

	psc := dep.Spec.Template.Spec.SecurityContext
	require.NotNil(t, psc, "pod SecurityContext must be set")
	require.NotNil(t, psc.RunAsNonRoot)
	require.True(t, *psc.RunAsNonRoot, "RunAsNonRoot=true required")
	require.NotNil(t, psc.SeccompProfile)
	require.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, psc.SeccompProfile.Type)

	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	csc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, csc, "container SecurityContext must be set")
	require.NotNil(t, csc.ReadOnlyRootFilesystem)
	require.True(t, *csc.ReadOnlyRootFilesystem, "ReadOnlyRootFilesystem=true required")
	require.NotNil(t, csc.AllowPrivilegeEscalation)
	require.False(t, *csc.AllowPrivilegeEscalation, "AllowPrivilegeEscalation=false required")
	require.NotNil(t, csc.Capabilities)
	require.Contains(t, csc.Capabilities.Drop, corev1.Capability("ALL"))
}

func TestBuildSecret_Naming(t *testing.T) {
	sec := BuildTokenSecret("t", "ns", "opaque-token", "tun-id-123")
	require.Equal(t, "cloudflared-token-t", sec.Name)
	require.Equal(t, "ns", sec.Namespace)
	require.Equal(t, []byte("opaque-token"), sec.Data["token"])
	require.Equal(t, "tun-id-123", sec.Annotations[annotationTokenTunnelID])
}

func TestBuildMetricsService_Naming(t *testing.T) {
	svc := BuildMetricsService("t", "ns")
	require.Equal(t, "cloudflared-t-metrics", svc.Name)
	require.Equal(t, "ns", svc.Namespace)
	require.Equal(t, int32(2000), svc.Spec.Ports[0].Port)
}

func TestDefaultCloudflaredImage_IsValidPin(t *testing.T) {
	require.Regexp(t,
		`^docker\.io/cloudflare/cloudflared:\d{4}\.\d+\.\d+$`,
		DefaultCloudflaredImage,
		"DefaultCloudflaredImage must pin a calendar-versioned cloudflare/cloudflared tag (Renovate-managed)")
}

func TestResolveImage_PerAxis(t *testing.T) {
	const def = "docker.io/cloudflare/cloudflared:2026.5.0"
	require.Equal(t, def, ResolveImage(nil, def))
	require.Equal(t, "docker.io/cloudflare/cloudflared:2026.5.0",
		ResolveImage(&v2alpha1.ConnectorImage{}, def))
	require.Equal(t, "mirror.example/cf/cloudflared:2026.5.0",
		ResolveImage(&v2alpha1.ConnectorImage{Repository: "mirror.example/cf/cloudflared"}, def))
	require.Equal(t, "docker.io/cloudflare/cloudflared:2026.6.0",
		ResolveImage(&v2alpha1.ConnectorImage{Tag: "2026.6.0"}, def))
}

func TestSplitImage(t *testing.T) {
	cases := []struct{ in, wantRepo, wantTag string }{
		{"cloudflared", "cloudflared", "latest"},
		{"cloudflare/cloudflared:2024.1.0", "cloudflare/cloudflared", "2024.1.0"},
		{"registry.example.com:5000/cloudflared", "registry.example.com:5000/cloudflared", "latest"},
		{"registry.example.com:5000/cloudflared:2024.1.0", "registry.example.com:5000/cloudflared", "2024.1.0"},
	}
	for _, c := range cases {
		repo, tag := splitImage(c.in)
		require.Equal(t, c.wantRepo, repo, c.in)
		require.Equal(t, c.wantTag, tag, c.in)
	}
}
