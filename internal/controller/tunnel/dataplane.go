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

// Package tunnel hosts the CloudflareTunnel reconciler. Source-object
// reconcilers (Service / Gateway / HTTPRoute / TLSRoute) land in subsequent
// bundle tasks and share this package.
package tunnel

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// DefaultCloudflaredImage is the operator's compile-time pin. The reconciler
// passes its configured DefaultImage through to BuildDeployment; this constant
// is exported so the manager setup wires the same value.
const DefaultCloudflaredImage = "docker.io/cloudflare/cloudflared:2026.4.1"

// dataplaneName returns the Deployment / dataplane resource basename for a
// given CloudflareTunnel. The 52-char cap on spec.name guarantees this stays
// within the 63-char DNS-1123 label limit ("cloudflared-" = 11 chars).
func dataplaneName(tn *v1alpha1.CloudflareTunnel) string {
	return "cloudflared-" + tn.Name
}

func tokenSecretName(tn *v1alpha1.CloudflareTunnel) string {
	return "cloudflared-token-" + tn.Name
}

func metricsServiceName(tn *v1alpha1.CloudflareTunnel) string {
	return "cloudflared-" + tn.Name + "-metrics"
}

// BuildDeployment renders the cloudflared Deployment for a tunnel. Pure
// builder — no IO, no apply. Caller is responsible for owner-refs and SSA.
//
// The pod-template-spec carries the Foundation hardening profile (runAsNon-
// Root, RuntimeDefault seccomp at the pod level; readOnlyRootFilesystem,
// dropped capabilities, AllowPrivilegeEscalation=false at the container
// level). Mirrors internal/bootstrap/deployments.go::BuildControllerDeployment
// so cloudflared Pods carry the same baseline as the operator itself.
//
// terminationGracePeriodSeconds = spec.connector.gracePeriodSeconds + 15,
// giving cloudflared the configured grace plus a 15-second buffer before
// kubelet sends SIGKILL.
//
// Image resolution combines a partial user override with defaults
// independently per axis (Repository / Tag): a user can override just the
// repository (private mirror) without losing the operator's pinned tag.
// See resolveImage.
//
// Resources combine user Requests + Limits with defaults independently
// per half: setting only Requests still gets the default Limits safety
// floor.
func BuildDeployment(tn *v1alpha1.CloudflareTunnel, defaultImage string) *appsv1.Deployment {
	labels := dataplaneLabels(tn)
	image := resolveImage(tn.Spec.Connector.Image, defaultImage)

	replicas := tn.Spec.Connector.Replicas
	grace := tn.Spec.Connector.GracePeriodSeconds
	terminationGrace := grace + 15

	args := []string{
		"tunnel",
		"--no-autoupdate",
		"--loglevel=" + tn.Spec.Connector.LogLevel,
		"--metrics=0.0.0.0:2000",
		"--protocol=" + tn.Spec.Connector.Protocol,
		"--grace-period=" + strconv.FormatInt(grace, 10) + "s",
		"run",
	}

	resources := resolveResources(tn.Spec.Connector.Resources)

	container := corev1.Container{
		Name:  "cloudflared",
		Image: image,
		Args:  args,
		Env: []corev1.EnvVar{{
			Name: "TUNNEL_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecretName(tn)},
					Key:                  "token",
				},
			},
		}},
		Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 2000}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("metrics")},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		Resources: resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}

	pod := corev1.PodSpec{
		TerminationGracePeriodSeconds: &terminationGrace,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptrBool(true),
			RunAsUser:    ptrInt64(65532),
			RunAsGroup:   ptrInt64(65532),
			FSGroup:      ptrInt64(65532),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		Containers:                []corev1.Container{container},
		NodeSelector:              tn.Spec.Connector.NodeSelector,
		Tolerations:               tn.Spec.Connector.Tolerations,
		Affinity:                  tn.Spec.Connector.Affinity,
		TopologySpreadConstraints: tn.Spec.Connector.TopologySpreadConstraints,
	}

	if ca := tn.Spec.Connector.OriginCASecretRef; ca != nil {
		pod.Volumes = []corev1.Volume{{
			Name: "origin-ca",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: ca.Name},
			},
		}}
		pod.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name: "origin-ca", MountPath: "/etc/cloudflared/ca", ReadOnly: true,
		}}
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataplaneName(tn),
			Namespace: tn.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}
}

// BuildTokenSecret renders the TUNNEL_TOKEN Secret. Stable name keyed off the
// CR name so the cloudflared Deployment's envFrom reference is deterministic.
// The Secret's Data["token"] is the connector-join token returned by
// GET /cfd_tunnel/{id}/token — opaque, never logged.
func BuildTokenSecret(tunnelName, namespace, token string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloudflared-token-" + tunnelName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"token": []byte(token)},
	}
}

// BuildMetricsService renders the operator-owned metrics ClusterIP Service
// fronting cloudflared's :2000 metrics endpoint.
func BuildMetricsService(tunnelName, namespace string) *corev1.Service {
	labels := map[string]string{
		"app.kubernetes.io/name":     "cloudflared",
		"app.kubernetes.io/instance": tunnelName,
	}
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloudflared-" + tunnelName + "-metrics",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name: "metrics", Port: 2000, TargetPort: intstr.FromString("metrics"),
			}},
		},
	}
}

func dataplaneLabels(tn *v1alpha1.CloudflareTunnel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "cloudflared",
		"app.kubernetes.io/instance": tn.Name,
		"app.kubernetes.io/part-of":  "cloudflare-operator",
	}
}

// resolveImage combines a partial override with the default image string
// independently per axis (Repository / Tag). Either half left unset on the
// override falls through to the default.
func resolveImage(override *v1alpha1.ConnectorImage, defaultImage string) string {
	repo, tag := splitImage(defaultImage)
	if override != nil {
		if override.Repository != "" {
			repo = override.Repository
		}
		if override.Tag != "" {
			tag = override.Tag
		}
	}
	return repo + ":" + tag
}

// splitImage splits a "<repo>:<tag>" reference. If no colon is present, the
// returned tag is "latest". Only the final colon is used as the separator so
// registries with explicit ports (e.g. registry.example.com:5000/image:tag)
// parse correctly.
func splitImage(s string) (string, string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:]
		}
	}
	return s, "latest"
}

// resolveResources applies safety floors independently per half: Requests and
// Limits each fill in if unset. A user who only sets Requests still gets the
// default Limits ceiling, and vice versa.
func resolveResources(user corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := user.DeepCopy()
	if out.Requests == nil {
		out.Requests = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		}
	}
	if out.Limits == nil {
		out.Limits = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}
	}
	return *out
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt64(i int64) *int64 { return &i }
