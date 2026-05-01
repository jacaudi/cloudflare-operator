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

package controller

import (
	"reflect"
	"strconv"
	"testing"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func tunnelFixture(enabled bool) *cloudflarev1alpha1.CloudflareTunnel {
	return &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "network", UID: "tunnel-uid"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "home",
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-api-token"},
			GeneratedSecretName: "home-tunnel-credentials",
			Connector: &cloudflarev1alpha1.ConnectorSpec{
				Enabled:  enabled,
				Replicas: 2,
			},
		},
	}
}

func TestConnectorNames(t *testing.T) {
	tun := tunnelFixture(true)
	n := ConnectorNames(tun)
	if n.Deployment != "home-connector" {
		t.Errorf("Deployment name = %q", n.Deployment)
	}
	if n.ConfigMap != "home-connector-config" {
		t.Errorf("ConfigMap name = %q", n.ConfigMap)
	}
	if n.ServiceAccount != "home-connector" {
		t.Errorf("ServiceAccount name = %q", n.ServiceAccount)
	}
	if n.PodDisruptionBudget != "home-connector-pdb" {
		t.Errorf("PodDisruptionBudget name = %q, want %q", n.PodDisruptionBudget, "home-connector-pdb")
	}
}

// TestConnectorNames_NameOverride verifies that spec.connector.nameOverride
// replaces the default "<tunnel-name>-connector" base across the Deployment,
// ServiceAccount, and ConfigMap. ConfigMap retains its "-config" suffix to
// disambiguate it from the Deployment/SA, which share the override name (#68).
func TestConnectorNames_NameOverride(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.NameOverride = "cloudflared-prod"

	n := ConnectorNames(tun)
	if n.Deployment != "cloudflared-prod" {
		t.Errorf("Deployment name = %q, want %q", n.Deployment, "cloudflared-prod")
	}
	if n.ServiceAccount != "cloudflared-prod" {
		t.Errorf("ServiceAccount name = %q, want %q", n.ServiceAccount, "cloudflared-prod")
	}
	if n.ConfigMap != "cloudflared-prod-config" {
		t.Errorf("ConfigMap name = %q, want %q", n.ConfigMap, "cloudflared-prod-config")
	}
}

// TestConnectorNames_NameOverrideEmpty verifies that an empty NameOverride
// (the zero value) falls back to the default "<tunnel-name>-connector" family.
func TestConnectorNames_NameOverrideEmpty(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.NameOverride = ""

	n := ConnectorNames(tun)
	if n.Deployment != "home-connector" {
		t.Errorf("Deployment name = %q, want default %q", n.Deployment, "home-connector")
	}
	if n.ConfigMap != "home-connector-config" {
		t.Errorf("ConfigMap name = %q, want default %q", n.ConfigMap, "home-connector-config")
	}
	if n.ServiceAccount != "home-connector" {
		t.Errorf("ServiceAccount name = %q, want default %q", n.ServiceAccount, "home-connector")
	}
}

// TestConnectorNames_NameOverrideNilConnector verifies that ConnectorNames
// is safe when tun.Spec.Connector is nil (e.g., connector disabled / unset).
func TestConnectorNames_NameOverrideNilConnector(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector = nil

	n := ConnectorNames(tun)
	if n.Deployment != "home-connector" {
		t.Errorf("Deployment name = %q, want default %q", n.Deployment, "home-connector")
	}
}

func TestBuildConnectorResources_Basic(t *testing.T) {
	tun := tunnelFixture(true)
	configYAML := []byte("ingress:\n  - service: http_status:404\n")
	configHash := "abc123"

	sa := BuildConnectorServiceAccount(tun)
	cm := BuildConnectorConfigMap(tun, configYAML, configHash)
	dep := BuildConnectorDeployment(tun, configHash)

	// ServiceAccount basics.
	if sa.Namespace != tun.Namespace {
		t.Errorf("SA namespace = %q, want %q", sa.Namespace, tun.Namespace)
	}
	if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].UID != tun.UID {
		t.Errorf("SA missing ownerRef to tunnel: %+v", sa.OwnerReferences)
	}

	// ConfigMap contains the rendered config + hash annotation.
	if cm.Data["config.yaml"] != string(configYAML) {
		t.Errorf("ConfigMap data[config.yaml] mismatch")
	}
	if cm.Annotations[AnnotationConfigHash] != configHash {
		t.Errorf("ConfigMap missing config-hash annotation: %v", cm.Annotations)
	}

	// Deployment basics.
	if *dep.Spec.Replicas != 2 {
		t.Errorf("Deployment replicas = %d", *dep.Spec.Replicas)
	}
	if dep.Spec.Template.Annotations[AnnotationConfigHash] != configHash {
		t.Errorf("pod template missing config-hash annotation")
	}
	if dep.Spec.Template.Spec.ServiceAccountName != sa.Name {
		t.Errorf("pod SA = %q, want %q", dep.Spec.Template.Spec.ServiceAccountName, sa.Name)
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	c := dep.Spec.Template.Spec.Containers[0]

	// Credentials Secret mounted (Volume + VolumeMount).
	foundCredsVol := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == tun.Spec.GeneratedSecretName {
			foundCredsVol = true
		}
	}
	if !foundCredsVol {
		t.Errorf("pod does not mount credentials Secret %q as a Volume", tun.Spec.GeneratedSecretName)
	}
	foundCredsMount := false
	for _, m := range c.VolumeMounts {
		if m.Name == "credentials" && m.MountPath == "/etc/cloudflared/credentials" && m.ReadOnly {
			foundCredsMount = true
		}
	}
	if !foundCredsMount {
		t.Errorf("container missing read-only VolumeMount of credentials at /etc/cloudflared/credentials: %+v", c.VolumeMounts)
	}

	// Config ConfigMap mounted (Volume + VolumeMount).
	foundConfigVol := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == ConnectorNames(tun).ConfigMap {
			foundConfigVol = true
		}
	}
	if !foundConfigVol {
		t.Errorf("pod does not mount config ConfigMap as a Volume")
	}
	foundConfigMount := false
	for _, m := range c.VolumeMounts {
		if m.Name == "config" && m.MountPath == "/etc/cloudflared" && m.ReadOnly {
			foundConfigMount = true
		}
	}
	if !foundConfigMount {
		t.Errorf("container missing read-only VolumeMount of config at /etc/cloudflared: %+v", c.VolumeMounts)
	}
}

func TestBuildConnectorDeployment_ImageDefault(t *testing.T) {
	tun := tunnelFixture(true)
	// No spec.connector.image at all — expect the compile-time default.
	dep := BuildConnectorDeployment(tun, "h")
	image := dep.Spec.Template.Spec.Containers[0].Image
	if image != DefaultConnectorImage {
		t.Errorf("image default = %q, want %q", image, DefaultConnectorImage)
	}
}

func TestBuildConnectorDeployment_ImageExplicit(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Image = &cloudflarev1alpha1.ConnectorImage{
		Repository: "quay.io/my/cloudflared",
		Tag:        "2026.3.0",
	}
	dep := BuildConnectorDeployment(tun, "h")
	if got, want := dep.Spec.Template.Spec.Containers[0].Image, "quay.io/my/cloudflared:2026.3.0"; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}
}

// TestBuildConnectorDeployment_ImageRepoOnly verifies that when the user sets
// only Image.Repository (no Tag), the default tag is combined with the
// user-supplied repository — NOT silently discarded in favor of the full
// default image reference.
func TestBuildConnectorDeployment_ImageRepoOnly(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Image = &cloudflarev1alpha1.ConnectorImage{
		Repository: "quay.io/mirror/cloudflared",
	}
	dep := BuildConnectorDeployment(tun, "h")
	got := dep.Spec.Template.Spec.Containers[0].Image
	want := "quay.io/mirror/cloudflared:2026.3.0"
	if got != want {
		t.Errorf("image = %q, want %q (user repo must combine with default tag)", got, want)
	}
}

// TestBuildConnectorDeployment_ImageTagOnly verifies that when the user sets
// only Image.Tag (no Repository), the default repo is combined with the
// user-supplied tag.
func TestBuildConnectorDeployment_ImageTagOnly(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Image = &cloudflarev1alpha1.ConnectorImage{
		Tag: "2026.4.1",
	}
	dep := BuildConnectorDeployment(tun, "h")
	got := dep.Spec.Template.Spec.Containers[0].Image
	want := "docker.io/cloudflare/cloudflared:2026.4.1"
	if got != want {
		t.Errorf("image = %q, want %q (user tag must combine with default repo)", got, want)
	}
}

func TestBuildConnectorDeployment_OwnerRefAndLabels(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")
	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].UID != tun.UID {
		t.Errorf("Deployment missing ownerRef to tunnel")
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/name":       "cloudflared",
		"app.kubernetes.io/instance":   "home",
		"app.kubernetes.io/managed-by": "cloudflare-operator",
		"cloudflare.io/tunnel":         "home",
	}
	got := dep.Labels
	// Pattern #6: assert total map length to catch label-bleed regressions.
	if len(got) != len(wantLabels) {
		t.Errorf("Deployment labels count = %d, want %d: %v", len(got), len(wantLabels), got)
	}
	for k, v := range wantLabels {
		if got[k] != v {
			t.Errorf("label %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestBuildConnectorDeployment_ReplicasZero verifies that Replicas=0 is
// preserved (not silently replaced by a default). The apiserver default of 2
// fires only on field-absent create; by the time the controller reads the spec
// the value is always meaningful, including 0.
func TestBuildConnectorDeployment_ReplicasZero(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Replicas = 0
	dep := BuildConnectorDeployment(tun, "h")
	if dep.Spec.Replicas == nil {
		t.Fatal("Deployment.Spec.Replicas is nil")
	}
	if *dep.Spec.Replicas != 0 {
		t.Errorf("Replicas = %d, want 0 (must preserve user intent)", *dep.Spec.Replicas)
	}
}

// TestBuildConnectorDeployment_NilConnector ensures BuildConnectorDeployment
// does not panic and defaults to 2 replicas when spec.connector is nil.
func TestBuildConnectorDeployment_NilConnector(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector = nil
	dep := BuildConnectorDeployment(tun, "h")
	if dep.Spec.Replicas == nil {
		t.Fatal("Deployment.Spec.Replicas is nil")
	}
	if *dep.Spec.Replicas != 2 {
		t.Errorf("Replicas with nil connector = %d, want 2", *dep.Spec.Replicas)
	}
}

// TestBuildConnectorDeployment_SecurityPosture asserts the full
// PodSecurityStandard "restricted" posture: runAsNonRoot, runAsUser/Group,
// drop-ALL caps, no privilege escalation, RO root FS, runtime-default seccomp.
func TestBuildConnectorDeployment_SecurityPosture(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")

	psc := dep.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("PodSecurityContext is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Errorf("pod must runAsNonRoot=true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 65532 {
		t.Errorf("pod RunAsUser = %v, want 65532", psc.RunAsUser)
	}
	if psc.RunAsGroup == nil || *psc.RunAsGroup != 65532 {
		t.Errorf("pod RunAsGroup = %v, want 65532", psc.RunAsGroup)
	}
	if psc.FSGroup != nil {
		t.Errorf("pod FSGroup must be unset for read-only mounts, got %d", *psc.FSGroup)
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod SeccompProfile = %+v, want RuntimeDefault", psc.SeccompProfile)
	}

	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	csc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	if csc == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
		t.Errorf("container must ReadOnlyRootFilesystem=true")
	}
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Errorf("container must AllowPrivilegeEscalation=false")
	}
	if csc.Capabilities == nil {
		t.Fatal("container Capabilities is nil")
	}
	wantDrop := []corev1.Capability{"ALL"}
	if !reflect.DeepEqual(csc.Capabilities.Drop, wantDrop) {
		t.Errorf("container Capabilities.Drop = %v, want %v", csc.Capabilities.Drop, wantDrop)
	}
	if len(csc.Capabilities.Add) != 0 {
		t.Errorf("container Capabilities.Add = %v, want empty", csc.Capabilities.Add)
	}
}

// TestBuildConnectorDeployment_ReadinessProbe asserts the cloudflared
// readiness probe targets the metrics server's /ready endpoint via httpGet.
// The exec form is rejected by current cloudflared releases without --metrics
// on the CLI; httpGet is the stable, documented surface (see issue #76).
func TestBuildConnectorDeployment_ReadinessProbe(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	probe := dep.Spec.Template.Spec.Containers[0].ReadinessProbe
	if probe == nil {
		t.Fatal("ReadinessProbe is nil")
	}
	if probe.HTTPGet == nil {
		t.Fatal("ReadinessProbe.HTTPGet is nil; want httpGet probe (issue #76)")
	}
	if probe.HTTPGet.Path != "/ready" {
		t.Errorf("ReadinessProbe.HTTPGet.Path = %q, want /ready", probe.HTTPGet.Path)
	}
	if probe.HTTPGet.Port.IntValue() != connectorMetricsPort {
		t.Errorf("ReadinessProbe.HTTPGet.Port = %v, want %d", probe.HTTPGet.Port, connectorMetricsPort)
	}
	if probe.PeriodSeconds != 10 {
		t.Errorf("ReadinessProbe.PeriodSeconds = %d, want 10", probe.PeriodSeconds)
	}
}

// TestBuildConnectorDeployment_MetricsArgsAndPort asserts cloudflared is
// started with --metrics bound on the well-known port and that the matching
// containerPort is declared. Both are required for the readiness probe to
// resolve and for ServiceMonitor scrapes to land.
func TestBuildConnectorDeployment_MetricsArgsAndPort(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")
	c := dep.Spec.Template.Spec.Containers[0]

	wantArg := "0.0.0.0:" + strconv.Itoa(connectorMetricsPort)
	foundMetricsFlag := false
	for i, a := range c.Args {
		if a == "--metrics" {
			if i+1 < len(c.Args) && c.Args[i+1] == wantArg {
				foundMetricsFlag = true
			}
			break
		}
	}
	if !foundMetricsFlag {
		t.Errorf("Args = %v; expected '--metrics %s' (issue #76)", c.Args, wantArg)
	}

	if len(c.Ports) != 1 || c.Ports[0].Name != "metrics" || c.Ports[0].ContainerPort != connectorMetricsPort {
		t.Errorf("expected single 'metrics' containerPort %d, got %v", connectorMetricsPort, c.Ports)
	}
}

// TestBuildConnectorDeployment_RolloutStrategy asserts the Deployment uses an
// explicit RollingUpdate with maxUnavailable=maxSurge=1 and a 10s
// minReadySeconds grace period, so single-replica image upgrades surge-then-
// terminate and never run zero ready tunnels (issue #75).
func TestBuildConnectorDeployment_RolloutStrategy(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")

	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("Strategy.Type = %v, want RollingUpdate", dep.Spec.Strategy.Type)
	}
	ru := dep.Spec.Strategy.RollingUpdate
	if ru == nil {
		t.Fatal("Strategy.RollingUpdate is nil")
	}
	if ru.MaxUnavailable == nil || ru.MaxUnavailable.IntValue() != 1 {
		t.Errorf("Strategy.RollingUpdate.MaxUnavailable = %v, want 1", ru.MaxUnavailable)
	}
	if ru.MaxSurge == nil || ru.MaxSurge.IntValue() != 1 {
		t.Errorf("Strategy.RollingUpdate.MaxSurge = %v, want 1", ru.MaxSurge)
	}
	if dep.Spec.MinReadySeconds != 10 {
		t.Errorf("MinReadySeconds = %d, want 10", dep.Spec.MinReadySeconds)
	}
}

// TestBuildConnectorDeployment_PartialResources ensures Requests and Limits
// are defaulted independently. A user who specifies only Requests must still
// receive the Memory limit safety floor.
func TestBuildConnectorDeployment_PartialResources(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}
	dep := BuildConnectorDeployment(tun, "h")
	got := dep.Spec.Template.Spec.Containers[0].Resources

	// User-supplied Requests must be preserved exactly.
	if q := got.Requests[corev1.ResourceCPU]; !q.Equal(resource.MustParse("50m")) {
		t.Errorf("Requests[CPU] = %s, want 50m", q.String())
	}
	if q := got.Requests[corev1.ResourceMemory]; !q.Equal(resource.MustParse("64Mi")) {
		t.Errorf("Requests[Memory] = %s, want 64Mi", q.String())
	}
	// Limits default must still fire (Memory only).
	if got.Limits == nil {
		t.Fatal("Limits is nil; default Memory limit did not fire")
	}
	if q := got.Limits[corev1.ResourceMemory]; !q.Equal(resource.MustParse("256Mi")) {
		t.Errorf("Limits[Memory] = %s, want 256Mi", q.String())
	}
}

// TestBuildConnectorDeployment_PartialResources_LimitsOnly mirrors the above
// for the user-set-only-Limits case: Requests defaults must still fire.
func TestBuildConnectorDeployment_PartialResources_LimitsOnly(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
	dep := BuildConnectorDeployment(tun, "h")
	got := dep.Spec.Template.Spec.Containers[0].Resources

	if got.Requests == nil {
		t.Fatal("Requests is nil; default did not fire")
	}
	if q := got.Requests[corev1.ResourceCPU]; !q.Equal(resource.MustParse("10m")) {
		t.Errorf("Requests[CPU] = %s, want 10m", q.String())
	}
	if q := got.Requests[corev1.ResourceMemory]; !q.Equal(resource.MustParse("128Mi")) {
		t.Errorf("Requests[Memory] = %s, want 128Mi", q.String())
	}
	// User-supplied Limits preserved.
	if q := got.Limits[corev1.ResourceMemory]; !q.Equal(resource.MustParse("512Mi")) {
		t.Errorf("Limits[Memory] = %s, want 512Mi", q.String())
	}
}

// TestBuildConnectorPodDisruptionBudget_NilWhenReplicasOne asserts the build
// function returns nil at replicas==1 so the reconciler treats this as
// "ensure absent" — a minAvailable:1 PDB on a single-replica deployment
// blocks all voluntary disruptions and is strictly worse than no PDB.
func TestBuildConnectorPodDisruptionBudget_NilWhenReplicasOne(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.Replicas = 1
	if got := BuildConnectorPodDisruptionBudget(tun); got != nil {
		t.Errorf("expected nil at replicas==1, got %+v", got)
	}
}

// TestBuildConnectorPodDisruptionBudget_NilWhenConnectorNil covers the
// defensive case where Spec.Connector is unset (connector disabled).
func TestBuildConnectorPodDisruptionBudget_NilWhenConnectorNil(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector = nil
	if got := BuildConnectorPodDisruptionBudget(tun); got != nil {
		t.Errorf("expected nil when connector spec absent, got %+v", got)
	}
}

// TestBuildConnectorPodDisruptionBudget_AtReplicasTwo locks the shape:
// minAvailable=1, selector matches connector labels exactly, owner-ref to
// the tunnel, name picks up the standard "<base>-pdb" suffix.
func TestBuildConnectorPodDisruptionBudget_AtReplicasTwo(t *testing.T) {
	tun := tunnelFixture(true) // Replicas == 2 in the fixture
	pdb := BuildConnectorPodDisruptionBudget(tun)
	if pdb == nil {
		t.Fatal("expected non-nil PDB at replicas==2")
	}
	if pdb.Name != "home-connector-pdb" {
		t.Errorf("Name = %q, want %q", pdb.Name, "home-connector-pdb")
	}
	if pdb.Namespace != tun.Namespace {
		t.Errorf("Namespace = %q, want %q", pdb.Namespace, tun.Namespace)
	}
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Errorf("MinAvailable = %v, want 1", pdb.Spec.MinAvailable)
	}
	if pdb.Spec.MaxUnavailable != nil {
		t.Errorf("MaxUnavailable = %v, want nil (we set MinAvailable)", pdb.Spec.MaxUnavailable)
	}
	wantSelector := connectorLabels(tun)
	if !reflect.DeepEqual(pdb.Spec.Selector.MatchLabels, wantSelector) {
		t.Errorf("Selector.MatchLabels = %v, want %v", pdb.Spec.Selector.MatchLabels, wantSelector)
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/name":       "cloudflared",
		"app.kubernetes.io/instance":   tun.Name,
		"app.kubernetes.io/managed-by": "cloudflare-operator",
		"cloudflare.io/tunnel":         tun.Name,
	}
	gotLabels := pdb.Labels
	// Pattern #6: assert total map length to catch label-bleed regressions.
	if len(gotLabels) != len(wantLabels) {
		t.Errorf("PDB labels count = %d, want %d: %v", len(gotLabels), len(wantLabels), gotLabels)
	}
	for k, v := range wantLabels {
		if gotLabels[k] != v {
			t.Errorf("label %q = %q, want %q", k, gotLabels[k], v)
		}
	}
	if len(pdb.OwnerReferences) != 1 || pdb.OwnerReferences[0].UID != tun.UID {
		t.Errorf("OwnerReferences missing tunnel ref: %+v", pdb.OwnerReferences)
	}
}

// TestBuildConnectorPodDisruptionBudget_NameOverride verifies that
// spec.connector.nameOverride flows through to the PDB name (since
// ConnectorNames.PodDisruptionBudget derives from the same base).
func TestBuildConnectorPodDisruptionBudget_NameOverride(t *testing.T) {
	tun := tunnelFixture(true)
	tun.Spec.Connector.NameOverride = "cloudflared-prod"
	pdb := BuildConnectorPodDisruptionBudget(tun)
	if pdb == nil {
		t.Fatal("expected non-nil PDB at replicas==2")
	}
	if pdb.Name != "cloudflared-prod-pdb" {
		t.Errorf("Name = %q, want %q", pdb.Name, "cloudflared-prod-pdb")
	}
}

// TestBuildConnectorDeployment_ArgsExact pins the cloudflared Args. The
// credentials path is in config.yaml (see Aggregate), so --credentials-file
// must NOT appear in Args (#58 follow-up: single source of truth for
// identity). --metrics is required for the readiness probe to resolve (#76).
func TestBuildConnectorDeployment_ArgsExact(t *testing.T) {
	tun := tunnelFixture(true)
	dep := BuildConnectorDeployment(tun, "h")
	got := dep.Spec.Template.Spec.Containers[0].Args
	want := []string{
		"tunnel",
		"--config", "/etc/cloudflared/config.yaml",
		"--metrics", "0.0.0.0:" + strconv.Itoa(connectorMetricsPort),
		"run",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--credentials-file" {
			t.Errorf("--credentials-file must not appear in Args: %v", got)
		}
	}
}
