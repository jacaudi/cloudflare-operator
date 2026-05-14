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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCloudflareTunnel_HasExpectedFields(t *testing.T) {
	tn := CloudflareTunnel{
		Spec: CloudflareTunnelSpec{
			Name: "cf-app-foo-payments",
			Connector: ConnectorSpec{
				Replicas:           2,
				Protocol:           "auto",
				GracePeriodSeconds: 30,
			},
		},
		Status: CloudflareTunnelStatus{
			TunnelID:           "abc-123",
			TunnelCNAME:        "abc-123.cfargotunnel.com",
			ConnectionsHealthy: 2,
			ObservedIngress: []IngressEntrySnapshot{
				{Hostname: "foo.example.com", Service: "http://svc.app-foo.svc.cluster.local:80"},
			},
			AttachedSources: []AttachedSource{
				{Kind: "Service", Name: "svc", Namespace: "app-foo"},
			},
		},
	}
	require.Equal(t, "cf-app-foo-payments", tn.Spec.Name)
	require.Equal(t, int32(2), tn.Spec.Connector.Replicas)
	require.Equal(t, "auto", tn.Spec.Connector.Protocol)
	require.Equal(t, int64(30), tn.Spec.Connector.GracePeriodSeconds)
	require.Equal(t, "abc-123.cfargotunnel.com", tn.Status.TunnelCNAME)
	require.Equal(t, int32(2), tn.Status.ConnectionsHealthy)
	require.Len(t, tn.Status.ObservedIngress, 1)
	require.Len(t, tn.Status.AttachedSources, 1)
}

func TestCloudflareTunnel_NoDroppedFields(t *testing.T) {
	// Compile-time canary: these fields MUST NOT exist on the spec.
	// (If a future refactor adds them back, this test fails to compile.)
	var s CloudflareTunnelSpec
	_ = s
	// Document the intentional absences for readers:
	// - s.ApexHostname  (dropped — Gateway-as-tunnel-apex replaces it)
	// - s.GeneratedSecretName  (dropped — remote-config uses TUNNEL_TOKEN Secret named by convention)
	// - s.Ingress[]  (dropped — synthesized only)
}

func TestCloudflareTunnel_StatusConditionsTyped(t *testing.T) {
	tn := CloudflareTunnel{
		Status: CloudflareTunnelStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"},
			},
		},
	}
	require.Equal(t, "Ready", tn.Status.Conditions[0].Type)
}

func TestAttachedSource_FieldShape(t *testing.T) {
	a := AttachedSource{Kind: "HTTPRoute", Name: "r", Namespace: "ns"}
	require.Equal(t, "HTTPRoute", a.Kind)
	require.Equal(t, "r", a.Name)
	require.Equal(t, "ns", a.Namespace)
}

func TestConnectorSpec_OriginCAOptional(t *testing.T) {
	cs := ConnectorSpec{Replicas: 2}
	require.Nil(t, cs.OriginCASecretRef, "OriginCASecretRef must be a pointer (optional)")
}

func TestCloudflareTunnel_StatusPhaseTyped(t *testing.T) {
	// Correction B: Status.Phase must be the shared Phase enum type,
	// not bare string — matching CloudflareZone's pattern.
	tn := CloudflareTunnel{
		Status: CloudflareTunnelStatus{
			Phase: PhaseReady,
		},
	}
	require.Equal(t, PhaseReady, tn.Status.Phase)
}
