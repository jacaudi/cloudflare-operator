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

// Package bootstrap is the meta-operator reconciler. It SSAs the domain CRDs
// and the zone / tunnel controller Deployments based on the CloudflareOperator
// CR.
package bootstrap

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// BuildArgs is the resolved input set for a single controller Deployment.
type BuildArgs struct {
	Bundle         string // "zone" | "tunnel"
	Namespace      string
	Image          string
	Replicas       int32
	LogLevel       string
	MetricsAddress string
	HealthAddress  string
	LeaderElection bool
	// Default-credential env passthrough. The meta-operator fills these from
	// its flags/env (chart-set credential Secret coordinates) so spawned
	// controllers get CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID via
	// valueFrom.secretKeyRef; per-CR overrides still work at reconcile time
	// via LoadCredentialsHierarchical.
	CredentialsSecretName   string
	CredentialsTokenKey     string
	CredentialsAccountIDKey string
}

// ApplyControllerSpec resolves user-supplied overrides against operator defaults.
// Image falls back to defaultImage (the meta-operator's own image).
//
// Note on Replicas: a zero value is treated as "unset, use default 1". Users
// who want to scale a bundle to zero set Enabled: false (which fully
// deprovisions the controller Deployment); Replicas: 0 with Enabled: true
// is not a supported configuration.
func ApplyControllerSpec(spec v2alpha1.ControllerSpec, defaultImage string) BuildArgs {
	img := spec.Image
	if img == "" {
		img = defaultImage
	}
	reps := spec.Replicas
	if reps == 0 {
		reps = 1
	}
	level := spec.LogLevel
	if level == "" {
		level = "info"
	}
	return BuildArgs{
		Image:    img,
		Replicas: reps,
		LogLevel: level,
	}
}

// BuildControllerDeployment renders a Deployment for the given bundle.
func BuildControllerDeployment(a BuildArgs) *appsv1.Deployment {
	name := "cloudflare-" + a.Bundle + "-controller"
	labels := map[string]string{
		"app.kubernetes.io/name":      name,
		"app.kubernetes.io/component": a.Bundle + "-controller",
		"app.kubernetes.io/part-of":   "cloudflare-operator",
	}

	args := []string{
		"--mode=" + a.Bundle,
		"--log-level=" + a.LogLevel,
	}
	if a.MetricsAddress != "" {
		args = append(args, "--metrics-address="+a.MetricsAddress)
	}
	if a.HealthAddress != "" {
		args = append(args, "--health-address="+a.HealthAddress)
	}
	args = append(args, fmt.Sprintf("--leader-election=%t", a.LeaderElection))

	// Env vars. Spawned controllers read these at startup as the default
	// credentials (both sourced from the chart-set credential Secret via
	// valueFrom.secretKeyRef — no plaintext in the PodSpec); per-CR overrides
	// still work at reconcile time via LoadCredentialsHierarchical.
	var envVars []corev1.EnvVar
	if a.CredentialsSecretName != "" {
		tokenKey := a.CredentialsTokenKey
		if tokenKey == "" {
			tokenKey = "token"
		}
		accountKey := a.CredentialsAccountIDKey
		if accountKey == "" {
			accountKey = "accountID"
		}
		envVars = []corev1.EnvVar{
			{Name: "CLOUDFLARE_API_TOKEN", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: a.CredentialsSecretName},
					Key:                  tokenKey,
				}}},
			{Name: "CLOUDFLARE_ACCOUNT_ID", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: a.CredentialsSecretName},
					Key:                  accountKey,
				}}},
		}
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: a.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(a.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: "cloudflare-operator",
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrBool(true),
						RunAsUser:    ptrInt64(65532),
						RunAsGroup:   ptrInt64(65532),
						FSGroup:      ptrInt64(65532),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:  "manager",
						Image: a.Image,
						Args:  args,
						Env:   envVars,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrBool(false),
							ReadOnlyRootFilesystem:   ptrBool(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
						Ports: []corev1.ContainerPort{
							{Name: "metrics", ContainerPort: 8080},
							{Name: "health", ContainerPort: 8081},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/readyz",
									Port: intstr.FromString("health"),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt64(i int64) *int64 { return &i }
