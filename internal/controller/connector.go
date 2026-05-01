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
	"fmt"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// connectorMetricsPort is the bind port for cloudflared's metrics server.
// The cloudflared CLI requires --metrics to be passed for `tunnel ... ready`
// to function and for the kubelet to probe /ready. The port is fixed so the
// container port, args, and readiness probe stay in lock-step.
const connectorMetricsPort = 20241

// Default connector image components. DefaultConnectorImage is built from
// these so version bumps stay consistent with the partial-image fallback in
// resolveConnectorImage. Bumped per operator release.
const (
	defaultConnectorRepo = "docker.io/cloudflare/cloudflared"
	defaultConnectorTag  = "2026.3.0"
)

// DefaultConnectorImage is used when spec.connector.image is unset.
const DefaultConnectorImage = defaultConnectorRepo + ":" + defaultConnectorTag

// ConnectorResourceNames are the deterministic names of the resources the
// connector sub-reconciler manages for a given CloudflareTunnel.
type ConnectorResourceNames struct {
	Deployment          string
	ConfigMap           string
	ServiceAccount      string
	PodDisruptionBudget string
}

// ConnectorNames returns the deterministic resource names for tun.
//
// When tun.Spec.Connector.NameOverride is set, the Deployment and
// ServiceAccount are named exactly NameOverride and the ConfigMap is named
// "<NameOverride>-config". When unset, names fall back to the
// "<tunnel.metadata.name>-connector" family.
func ConnectorNames(tun *cloudflarev1alpha1.CloudflareTunnel) ConnectorResourceNames {
	base := tun.Name + "-connector"
	if tun.Spec.Connector != nil && tun.Spec.Connector.NameOverride != "" {
		base = tun.Spec.Connector.NameOverride
	}
	return ConnectorResourceNames{
		Deployment:          base,
		ConfigMap:           base + "-config",
		ServiceAccount:      base,
		PodDisruptionBudget: base + "-pdb",
	}
}

func connectorLabels(tun *cloudflarev1alpha1.CloudflareTunnel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "cloudflared",
		"app.kubernetes.io/instance":   tun.Name,
		"app.kubernetes.io/managed-by": "cloudflare-operator",
		"cloudflare.io/tunnel":         tun.Name,
	}
}

func connectorOwnerRef(tun *cloudflarev1alpha1.CloudflareTunnel) []metav1.OwnerReference {
	controller := true
	blockDel := true
	return []metav1.OwnerReference{{
		APIVersion:         cloudflarev1alpha1.GroupVersion.String(),
		Kind:               "CloudflareTunnel",
		Name:               tun.Name,
		UID:                tun.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockDel,
	}}
}

// resolveConnectorImage picks the image reference for the cloudflared
// container. The four cases:
//
//  1. img == nil                          -> DefaultConnectorImage
//  2. img.Repository == "" && Tag == ""   -> DefaultConnectorImage
//  3. img.Repository set, Tag == ""       -> "<repo>:" + defaultConnectorTag
//  4. img.Repository == "", Tag set       -> defaultConnectorRepo + ":<tag>"
//  5. both set                            -> "<repo>:<tag>"
//
// Cases 3 and 4 deliberately combine the user-supplied half with the default
// for the other half, so partial overrides do not silently discard user input.
func resolveConnectorImage(img *cloudflarev1alpha1.ConnectorImage) string {
	if img == nil {
		return DefaultConnectorImage
	}
	repo := img.Repository
	if repo == "" {
		repo = defaultConnectorRepo
	}
	tag := img.Tag
	if tag == "" {
		tag = defaultConnectorTag
	}
	return fmt.Sprintf("%s:%s", repo, tag)
}

// BuildConnectorServiceAccount produces the desired ServiceAccount for tun.
func BuildConnectorServiceAccount(tun *cloudflarev1alpha1.CloudflareTunnel) *corev1.ServiceAccount {
	n := ConnectorNames(tun)
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            n.ServiceAccount,
			Namespace:       tun.Namespace,
			Labels:          connectorLabels(tun),
			OwnerReferences: connectorOwnerRef(tun),
		},
	}
}

// BuildConnectorConfigMap produces the desired ConfigMap carrying the
// rendered cloudflared config.yaml.
func BuildConnectorConfigMap(tun *cloudflarev1alpha1.CloudflareTunnel, renderedConfig []byte, configHash string) *corev1.ConfigMap {
	n := ConnectorNames(tun)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            n.ConfigMap,
			Namespace:       tun.Namespace,
			Labels:          connectorLabels(tun),
			Annotations:     map[string]string{AnnotationConfigHash: configHash},
			OwnerReferences: connectorOwnerRef(tun),
		},
		Data: map[string]string{
			"config.yaml": string(renderedConfig),
		},
	}
}

// BuildConnectorPodDisruptionBudget returns the PDB that protects the
// cloudflared connector deployment from voluntary disruption (node drain,
// autoscaler scale-down, manual evict).
//
// Returns nil when:
//   - tun.Spec.Connector is unset (connector disabled), or
//   - replicas < 2.
//
// At replicas == 1 a minAvailable:1 PDB blocks all voluntary disruptions
// (strictly worse than no PDB) and maxUnavailable:1 permits all evictions
// (no protection). Skipping the PDB entirely is the correct call. The
// reconciler treats nil as "ensure absent" so transitions from replicas:2
// to replicas:1 cleanly delete the PDB.
func BuildConnectorPodDisruptionBudget(tun *cloudflarev1alpha1.CloudflareTunnel) *policyv1.PodDisruptionBudget {
	if tun.Spec.Connector == nil || tun.Spec.Connector.Replicas < 2 {
		return nil
	}
	n := ConnectorNames(tun)
	minAvail := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:            n.PodDisruptionBudget,
			Namespace:       tun.Namespace,
			Labels:          connectorLabels(tun),
			OwnerReferences: connectorOwnerRef(tun),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvail,
			Selector: &metav1.LabelSelector{
				MatchLabels: connectorLabels(tun),
			},
		},
	}
}

// BuildConnectorDeployment produces the desired Deployment running cloudflared.
//
// Replicas: when cspec is nil (defensive path), default to 2. When cspec is
// non-nil, use cspec.Replicas directly — this preserves a user-set value of 0
// (scale-down intent). The apiserver default of 2 fires only on field-absent
// creates, so by the time the controller reads the spec the value is always
// meaningful.
//
// Image: see resolveConnectorImage for the four-case partial-override matrix.
//
// Resources: cspec.Resources.Requests and cspec.Resources.Limits are defaulted
// independently so a user supplying only Requests still gets the Memory limit
// safety floor (and vice versa). Defaults: 10m CPU + 128Mi Memory requests,
// 256Mi Memory limit.
//
// Note: cspec.Resources is shallow-copied into the container; ResourceList and
// other map fields share storage with the input. Build* functions never mutate
// the spec in place, so this aliasing is safe.
func BuildConnectorDeployment(tun *cloudflarev1alpha1.CloudflareTunnel, configHash string) *appsv1.Deployment {
	n := ConnectorNames(tun)
	cspec := tun.Spec.Connector

	var replicas int32
	if cspec == nil {
		replicas = 2
		cspec = &cloudflarev1alpha1.ConnectorSpec{}
	} else {
		replicas = cspec.Replicas
	}

	image := resolveConnectorImage(cspec.Image)

	labels := connectorLabels(tun)
	nonRoot := true
	runAsUID := int64(65532)
	readOnlyFS := true
	privEsc := false

	// Independently default Requests and Limits so partial user input still
	// receives the corresponding safety floor for the unset half. Aliasing the
	// user's ResourceList maps is intentional (no in-place mutation occurs).
	containerResources := cspec.Resources
	if containerResources.Requests == nil {
		containerResources.Requests = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		}
	}
	if containerResources.Limits == nil {
		containerResources.Limits = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		}
	}

	podSpec := corev1.PodSpec{
		ServiceAccountName:        n.ServiceAccount,
		NodeSelector:              cspec.NodeSelector,
		Tolerations:               cspec.Tolerations,
		Affinity:                  cspec.Affinity,
		TopologySpreadConstraints: cspec.TopologySpreadConstraints,
		// FSGroup is intentionally unset: both volumes are read-only, and
		// FSGroup forces a recursive chown on some CSI drivers.
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: &nonRoot,
			RunAsUser:    &runAsUID,
			RunAsGroup:   &runAsUID,
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: tun.Spec.GeneratedSecretName,
					},
				},
			},
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: n.ConfigMap},
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name:      "cloudflared",
				Image:     image,
				Resources: containerResources,
				Args: []string{
					"tunnel",
					"--config", "/etc/cloudflared/config.yaml",
					"--metrics", fmt.Sprintf("0.0.0.0:%d", connectorMetricsPort),
					"run",
				},
				Ports: []corev1.ContainerPort{
					{
						Name:          "metrics",
						ContainerPort: connectorMetricsPort,
						Protocol:      corev1.ProtocolTCP,
					},
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &nonRoot,
					ReadOnlyRootFilesystem:   &readOnlyFS,
					AllowPrivilegeEscalation: &privEsc,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "config", MountPath: "/etc/cloudflared", ReadOnly: true},
					{Name: "credentials", MountPath: "/etc/cloudflared/credentials", ReadOnly: true},
				},
				// Readiness probes the metrics server's /ready endpoint, which
				// reports whether cloudflared has registered tunnel connections
				// to the Cloudflare edge. The exec form (`cloudflared tunnel
				// ready`) requires --metrics on the CLI and is brittle across
				// cloudflared versions; httpGet is the documented stable surface.
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt(connectorMetricsPort),
						},
					},
					PeriodSeconds: 10,
				},
			},
		},
	}

	// Rollout strategy: at most one replica may be unavailable, and at most one
	// surge replica is created. With replicas==1 this gives surge-then-terminate
	// ordering so an image upgrade never runs zero ready tunnels. With replicas>1
	// it is at least as safe as the percentage-based default and removes the
	// replica-count dependency.
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            n.Deployment,
			Namespace:       tun.Namespace,
			Labels:          labels,
			OwnerReferences: connectorOwnerRef(tun),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			// 10s grace before kubelet flips the new replica to Ready. cloudflared
			// reports /ready as soon as the first edge connection is registered,
			// but the tunnel only becomes truly redundant after multiple
			// connections are up — this gap covers that window.
			MinReadySeconds: 10,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						AnnotationConfigHash: configHash,
					},
				},
				Spec: podSpec,
			},
		},
	}
}
