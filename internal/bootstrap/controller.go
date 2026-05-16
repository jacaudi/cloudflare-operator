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

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// Reconciler reconciles the singleton CloudflareOperator CR.
type Reconciler struct {
	Client            client.Client
	Scheme            *runtime.Scheme
	OperatorNamespace string // namespace the meta-operator runs in
	OperatorImage     string // image to use when spec.controllers.X.image is empty
}

// SetupWithManager registers the reconciler with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.CloudflareOperator{}).
		Owns(&apiextv1.CustomResourceDefinition{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// Reconcile drives the CloudflareOperator → CRDs + Deployments flow.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("op", req.Name)

	var op v1alpha1.CloudflareOperator
	if err := r.Client.Get(ctx, req.NamespacedName, &op); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if op.Name != v1alpha1.CloudflareOperatorSingletonName {
		return r.markIgnored(ctx, &op)
	}

	originalStatus := *op.Status.DeepCopy()

	installedCRDs, err := r.reconcileCRDs(ctx, &op, logger)
	if err != nil {
		return r.markFailure(ctx, &op, conventions.ReasonBundlesInstalled, err.Error())
	}

	installedBundles, err := r.reconcileDeployments(ctx, &op, logger)
	if err != nil {
		return r.markFailure(ctx, &op, conventions.ReasonDeploymentsReady, err.Error())
	}

	op.Status.InstalledCRDs = installedCRDs
	op.Status.InstalledBundles = installedBundles
	op.Status.ObservedGeneration = op.Generation
	op.Status.Conditions = reconcile.SetReady(op.Status.Conditions, metav1.ConditionTrue, conventions.ReasonReady, "bundles installed and deployments running")
	// Singleton reconciles infrequently, but mirror the zone/tunnel
	// change-detection gate so an unchanged pass is a no-op write
	// (avoids needless status churn / write amplification).
	if op.Generation != originalStatus.ObservedGeneration || !equality.Semantic.DeepEqual(originalStatus, op.Status) {
		if err := r.Client.Status().Update(ctx, &op); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reconcileCRDs SSAs the CRDs for enabled bundles and reports the list of
// installed CRD names.
func (r *Reconciler) reconcileCRDs(ctx context.Context, op *v1alpha1.CloudflareOperator, logger logr.Logger) ([]string, error) {
	var wanted []Bundle
	if op.Spec.Controllers.Zone.Enabled {
		wanted = append(wanted, BundleZone)
	}
	if op.Spec.Controllers.Tunnel.Enabled {
		wanted = append(wanted, BundleTunnel)
	}

	var installed []string
	for _, b := range wanted {
		crds, err := BundleCRDs(b)
		if err != nil {
			return nil, err
		}
		for _, crd := range crds {
			// Strip server-side fields before SSA so we set only what we own.
			crd.ResourceVersion = ""
			crd.UID = ""
			crd.ManagedFields = nil
			if err := reconcile.Apply(ctx, r.Client, crd); err != nil {
				return nil, fmt.Errorf("apply CRD %s: %w", crd.Name, err)
			}
			logger.V(1).Info("applied CRD", "name", crd.Name)
			installed = append(installed, crd.Name)
		}
	}
	return installed, nil
}

// reconcileDeployments SSAs the controller Deployment for each enabled bundle
// and deletes the Deployment + sweeps stale CRs for each disabled bundle.
func (r *Reconciler) reconcileDeployments(ctx context.Context, op *v1alpha1.CloudflareOperator, logger logr.Logger) ([]string, error) {
	type bundlePlan struct {
		bundle  Bundle
		enabled bool
		spec    v1alpha1.ControllerSpec
	}
	plans := []bundlePlan{
		{bundle: BundleZone, enabled: op.Spec.Controllers.Zone.Enabled, spec: op.Spec.Controllers.Zone},
		{bundle: BundleTunnel, enabled: op.Spec.Controllers.Tunnel.Enabled, spec: op.Spec.Controllers.Tunnel},
	}

	var installed []string
	for _, p := range plans {
		if p.enabled {
			args := ApplyControllerSpec(p.spec, r.OperatorImage)
			args.Bundle = string(p.bundle)
			args.Namespace = r.OperatorNamespace
			args.MetricsAddress = op.Spec.Observability.MetricsAddress
			args.HealthAddress = op.Spec.Observability.HealthAddress
			args.TokenSecretRef = op.Spec.Cloudflare.TokenSecretRef
			args.AccountID = op.Spec.Cloudflare.AccountID
			args.LeaderElection = op.Spec.Observability.LeaderElection.Enabled
			dep := BuildControllerDeployment(args)
			if err := reconcile.Apply(ctx, r.Client, dep); err != nil {
				return nil, fmt.Errorf("apply Deployment %s: %w", dep.Name, err)
			}
			installed = append(installed, string(p.bundle))
			logger.V(1).Info("applied Deployment", "bundle", p.bundle)
			continue
		}
		// Disabled: delete the Deployment and sweep stale CRs.
		depName := "cloudflare-" + string(p.bundle) + "-controller"
		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: depName, Namespace: r.OperatorNamespace}}
		if err := reconcile.WrapDeleteErr(r.Client.Delete(ctx, dep)); err != nil {
			return nil, fmt.Errorf("delete Deployment %s: %w", depName, err)
		}
		if err := r.sweepStaleCRs(ctx, p.bundle, logger); err != nil {
			return nil, fmt.Errorf("sweep stale CRs for bundle %s: %w", p.bundle, err)
		}
		logger.V(1).Info("deleted disabled bundle Deployment", "bundle", p.bundle)
	}
	return installed, nil
}

// sweepStaleCRs lists every CR of the given bundle's Kinds and stamps a
// ControllerOffline condition on each. This is a best-effort signal to users
// that their previously-Ready CRs are no longer being reconciled because the
// bundle was disabled. CRD-not-found errors are swallowed (the CRD may already
// be uninstalled).
//
// If any status update fails, the first error is tracked and returned so the
// parent reconcile requeues and retries the sweep.
//
// Uses unstructured so the bootstrap reconciler stays domain-agnostic — no
// dependency on spec 2 / spec 3 CRD Go types.
func (r *Reconciler) sweepStaleCRs(ctx context.Context, bundle Bundle, logger logr.Logger) error {
	var firstErr error
	for _, gvk := range bundleKinds(bundle) {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := r.Client.List(ctx, list); err != nil {
			if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
				continue // CRD not installed; nothing to sweep.
			}
			return err
		}
		for i := range list.Items {
			item := &list.Items[i]
			conds, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
			conds = upsertOfflineCondition(conds)
			if err := unstructured.SetNestedSlice(item.Object, conds, "status", "conditions"); err != nil {
				return err
			}
			if err := r.Client.Status().Update(ctx, item); err != nil {
				logger.V(1).Info("failed to stamp ControllerOffline; will retry", "kind", gvk.Kind, "name", item.GetName(), "err", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
	}
	return firstErr
}

// bundleKinds returns the GroupVersionKinds belonging to each bundle.
func bundleKinds(b Bundle) []schema.GroupVersionKind {
	gv := schema.GroupVersion{Group: "cloudflare.io", Version: "v1alpha1"}
	switch b {
	case BundleZone:
		return []schema.GroupVersionKind{
			gv.WithKind("CloudflareZone"),
			gv.WithKind("CloudflareZoneConfig"),
			gv.WithKind("CloudflareDNSRecord"),
			gv.WithKind("CloudflareRuleset"),
		}
	case BundleTunnel:
		return []schema.GroupVersionKind{gv.WithKind("CloudflareTunnel")}
	}
	return nil
}

// upsertOfflineCondition sets or replaces the Ready condition to
// (False, ControllerOffline). Delegates to the shared SetUnstructuredCondition
// helper which correctly preserves LastTransitionTime on no-op calls.
func upsertOfflineCondition(conds []interface{}) []interface{} {
	return reconcile.SetUnstructuredCondition(
		conds,
		conventions.ConditionTypeReady,
		"False",
		conventions.ReasonControllerOffline,
		"bundle disabled by CloudflareOperator; controller no longer reconciling",
	)
}

// markIgnored stamps an Ignored condition on non-singleton CRs.
func (r *Reconciler) markIgnored(ctx context.Context, op *v1alpha1.CloudflareOperator) (ctrl.Result, error) {
	op.Status.Conditions = reconcile.SetReady(op.Status.Conditions, metav1.ConditionFalse, conventions.ReasonIgnored,
		fmt.Sprintf("only the singleton CR named %q is reconciled", v1alpha1.CloudflareOperatorSingletonName))
	op.Status.ObservedGeneration = op.Generation
	if err := r.Client.Status().Update(ctx, op); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// markFailure stamps Ready=False with reason+msg and returns the FailReconcile result.
func (r *Reconciler) markFailure(ctx context.Context, op *v1alpha1.CloudflareOperator, reason, msg string) (ctrl.Result, error) {
	op.Status.Conditions = reconcile.SetReady(op.Status.Conditions, metav1.ConditionFalse, reason, msg)
	op.Status.ObservedGeneration = op.Generation
	if err := r.Client.Status().Update(ctx, op); err != nil {
		log.FromContext(ctx).Error(err, "status update failed in markFailure", "reason", reason)
	}
	return *reconcile.FailReconcile(ctx, reason, msg), nil
}
