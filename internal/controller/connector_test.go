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
	"testing"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	// Security posture
	psc := dep.Spec.Template.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Errorf("pod must runAsNonRoot=true")
	}
	if len(dep.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(dep.Spec.Template.Spec.Containers))
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("container must ReadOnlyRootFilesystem=true")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("container must AllowPrivilegeEscalation=false")
	}
	// Credentials Secret mounted.
	foundCreds := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == tun.Spec.GeneratedSecretName {
			foundCreds = true
		}
	}
	if !foundCreds {
		t.Errorf("pod does not mount credentials Secret %q", tun.Spec.GeneratedSecretName)
	}
	// Config ConfigMap mounted.
	foundConfig := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == ConnectorNames(tun).ConfigMap {
			foundConfig = true
		}
	}
	if !foundConfig {
		t.Errorf("pod does not mount config ConfigMap")
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
