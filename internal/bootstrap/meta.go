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
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// MetaReconciler ensures the zone/tunnel controller Deployments match Config.
// It owns no CR: it watches the two managed Deployments by name and re-applies
// (SSA) desired state on any change/delete (drift-correction), plus an initial
// sync at manager start. Helm owns the CRDs; this reconciler never installs them.
type MetaReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Config Config
}

func managedDeploymentName(bundle string) string { return "cloudflare-" + bundle + "-controller" }

// SetupWithManager registers the initial-sync runnable + the Deployment watch.
func (r *MetaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ns := r.Config.OperatorNamespace
	zoneName := managedDeploymentName("zone")
	tunName := managedDeploymentName("tunnel")
	pred := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == ns && (o.GetName() == zoneName || o.GetName() == tunName)
	})
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		return r.ensure(ctx)
	})); err != nil {
		return fmt.Errorf("add initial-sync runnable: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}, builder.WithPredicates(pred)).
		Complete(r)
}

// Reconcile re-runs ensure on any change to a managed Deployment.
func (r *MetaReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, r.ensure(ctx)
}

// ensure SSAs each enabled controller Deployment and deletes each disabled one.
// Idempotent; safe to call from both the initial runnable and the watch.
func (r *MetaReconciler) ensure(ctx context.Context) error {
	plans := []struct {
		bundle   string
		enabled  bool
		replicas int32
		logLevel string
	}{
		{"zone", r.Config.ZoneEnabled, r.Config.ZoneReplicas, r.Config.ZoneLogLevel},
		{"tunnel", r.Config.TunnelEnabled, r.Config.TunnelReplicas, r.Config.TunnelLogLevel},
	}
	for _, p := range plans {
		name := managedDeploymentName(p.bundle)
		if !p.enabled {
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Config.OperatorNamespace}}
			if err := reconcile.WrapDeleteErr(r.Client.Delete(ctx, dep)); err != nil {
				return fmt.Errorf("delete Deployment %s: %w", name, err)
			}
			continue
		}
		reps := p.replicas
		if reps < 1 {
			reps = 1
		}
		level := p.logLevel
		if level == "" {
			level = "info"
		}
		dep := BuildControllerDeployment(BuildArgs{
			Bundle:                       p.bundle,
			Namespace:                    r.Config.OperatorNamespace,
			Image:                        r.Config.OperatorImage,
			Replicas:                     reps,
			LogLevel:                     level,
			MetricsAddress:               r.Config.MetricsAddress,
			HealthAddress:                r.Config.HealthAddress,
			LeaderElection:               r.Config.LeaderElection,
			CredentialsSecretName:        r.Config.CredentialsSecretName,
			CredentialsTokenKey:          r.Config.CredentialsTokenKey,
			CredentialsAccountIDKey:      r.Config.CredentialsAccountIDKey,
			TunnelConnectorResourcesJSON: r.Config.TunnelConnectorResourcesJSON,
		})
		if err := reconcile.Apply(ctx, r.Client, dep); err != nil {
			return fmt.Errorf("apply Deployment %s: %w", name, err)
		}
	}
	return nil
}
