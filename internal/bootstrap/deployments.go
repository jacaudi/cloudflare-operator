// Package bootstrap is the meta-operator reconciler. It SSAs the domain CRDs
// and the zone / tunnel controller Deployments based on the CloudflareOperator
// CR.
package bootstrap

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	// Default-credential env passthrough. The bootstrap reconciler fills these
	// from the top-level CloudflareOperator.spec.cloudflare so controllers have
	// credentials at startup; per-CR overrides still work at reconcile time via
	// LoadCredentialsHierarchical.
	TokenSecretRef v1alpha1.SecretReference // sourced from top-level tokenSecretRef
	AccountID      string                   // sourced from top-level accountID
}

// ApplyControllerSpec resolves user-supplied overrides against operator defaults.
// Image falls back to defaultImage (the meta-operator's own image).
//
// Note on Replicas: a zero value is treated as "unset, use default 1". Users
// who want to scale a bundle to zero set Enabled: false (which fully
// deprovisions the controller Deployment); Replicas: 0 with Enabled: true
// is not a supported configuration.
func ApplyControllerSpec(spec v1alpha1.ControllerSpec, defaultImage string) BuildArgs {
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

	// Env vars. Controllers read these at startup as the default credentials;
	// per-CR overrides still work at reconcile time via LoadCredentialsHierarchical.
	envVars := []corev1.EnvVar{
		{Name: "CLOUDFLARE_ACCOUNT_ID", Value: a.AccountID},
	}
	if a.TokenSecretRef.Name != "" {
		key := a.TokenSecretRef.Key
		if key == "" {
			key = "token"
		}
		envVars = append(envVars, corev1.EnvVar{
			Name: "CLOUDFLARE_API_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: a.TokenSecretRef.Name},
					Key:                  key,
				},
			},
		})
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: a.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr32(a.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: "cloudflare-operator",
					Containers: []corev1.Container{{
						Name:  "manager",
						Image: a.Image,
						Args:  args,
						Env:   envVars,
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

func ptr32(v int32) *int32 { return &v }
