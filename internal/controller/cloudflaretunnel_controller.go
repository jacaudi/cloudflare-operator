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
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// CloudflareTunnelReconciler reconciles a CloudflareTunnel object
type CloudflareTunnelReconciler struct {
	client.Client
	// APIReader bypasses the manager's label-filtered cache for reads
	// that need to see the actual API-server state regardless of the
	// cloudflare.io/managed=true label gate (introduced in PR #87).
	// Used by ensureCredentialsSecretExists to detect pre-existing
	// unlabeled Secrets that the cache would otherwise hide.
	APIReader      client.Reader
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	ClientFactory  CredentialFactory
	TunnelClientFn func(apiToken string) cfclient.TunnelClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels/finalizers,verbs=update
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnelrules,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnelrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=configmaps;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareTunnel resource. It handles the full lifecycle of tunnels
// including creation, adoption of existing tunnels, credential Secret
// generation, and deletion.
func (r *CloudflareTunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the CR
	var tunnel cloudflarev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, req.NamespacedName, &tunnel); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	preStatus := tunnel.Status.DeepCopy()

	// 2. Handle deletion
	if !tunnel.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&tunnel, cloudflarev1alpha1.FinalizerName) {
			return r.reconcileDelete(ctx, &tunnel)
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&tunnel, cloudflarev1alpha1.FinalizerName) {
		controllerutil.AddFinalizer(&tunnel, cloudflarev1alpha1.FinalizerName)
		if err := r.Update(ctx, &tunnel); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 4. Get Cloudflare credentials (API token + Account ID)
	secretNs := secretRefNamespace(tunnel.Spec.SecretRef, tunnel.Namespace)
	creds, halt, err := LoadCredentials(ctx, r.Client, r.ClientFactory,
		tunnel.Spec.SecretRef.Name, secretNs,
		r.Recorder, &tunnel, &tunnel.Status.Conditions, 30*time.Second)
	if halt != nil {
		if err == nil {
			logger.V(1).Info("credential load failed; halting reconcile",
				"secret", tunnel.Spec.SecretRef.Name, "namespace", secretNs)
		} else {
			logger.Error(err, "credential load failed")
		}
		return *halt, err
	}
	if creds.AccountID == "" {
		err := fmt.Errorf("secret %s/%s does not contain %q key",
			secretNs, tunnel.Spec.SecretRef.Name, cfclient.SecretKeyAccountID)
		logger.Error(err, "missing Account ID")
		return failReconcile(ctx, r.Client, &tunnel, &tunnel.Status.Conditions,
			&tunnel.Status.Phase, cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the tunnel
	result, err := r.reconcileTunnel(ctx, &tunnel, r.tunnelClient(creds.APIToken), creds.AccountID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		routing := ClassifyCloudflareError(err)
		if routing.ResetRemoteID {
			tunnel.Status.TunnelID = ""
			tunnel.Status.TunnelCNAME = ""
		}
		eventReason := routing.Reason
		if eventReason == cloudflarev1alpha1.ReasonCloudflareError {
			eventReason = "SyncFailed" // preserve historical event name for unclassified failures
		}
		r.Recorder.Event(&tunnel, corev1.EventTypeWarning, eventReason, err.Error())
		requeue := routing.RequeueAfter
		// requeue==0 means either: immediate (RemoteGone, with ResetRemoteID true)
		// or "use my default" (catch-all, with ResetRemoteID false).
		if requeue == 0 && !routing.ResetRemoteID {
			requeue = time.Minute
		}
		return failReconcile(ctx, r.Client, &tunnel, &tunnel.Status.Conditions,
			&tunnel.Status.Phase, routing.Reason, err, requeue)
	}

	// 7. Set Ready and ObservedGeneration in-memory. If aggregation is not
	// applicable (no CNAME yet), persist now. Otherwise let writeTunnelAggStatus
	// (called inside ReconcileConnectorAndRules) perform the single terminal write
	// so there is only one Status().Update per successful reconcile.
	tunnel.Status.ObservedGeneration = tunnel.Generation
	status.SetReady(&tunnel.Status.Conditions, &tunnel.Status.Phase, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Tunnel synced", tunnel.Generation)

	// 8. Aggregate rules and reconcile connector — only once the tunnel has a
	// CNAME (i.e. provisioning succeeded at least once).
	if tunnel.Status.TunnelCNAME != "" {
		// 8a. Reconcile the apex CloudflareDNSRecord, if spec.apexHostname is
		// set. Apex reconciliation runs BEFORE connector/rules so that
		// writeTunnelAggStatus (called from ReconcileConnectorAndRules)
		// persists the apex condition + status atomically with the rest of
		// the terminal status write. Apex problems do NOT block the
		// connector/rules step or the tunnel's Ready condition (design D5)
		// — that includes plumbing flakes here, not just condition-level
		// failures returned via the apex condition. On plumbing error: log
		// + event + force a 30s requeue, but continue through the connector
		// path so writeTunnelAggStatus still fires.
		apexRes, apexErr := reconcileApexHostname(ctx, r.Client, &tunnel)
		if apexErr != nil {
			logger.Error(apexErr, "apex reconcile failed")
			r.Recorder.Event(&tunnel, corev1.EventTypeWarning, "ApexReconcileFailed", apexErr.Error())
			apexRes = ctrl.Result{RequeueAfter: 30 * time.Second}
		}

		if err := ReconcileConnectorAndRules(ctx, r.Client, &tunnel, preStatus); err != nil {
			logger.Error(err, "aggregator/connector failed")
			r.Recorder.Event(&tunnel, corev1.EventTypeWarning, "AggregationFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		// Combine the tunnel's natural requeue with the apex result. The
		// shorter of the two wins so the operator catches up promptly when
		// the apex needs a faster recheck (e.g. zone-not-ready).
		merged := result
		if apexRes.RequeueAfter > 0 && (merged.RequeueAfter == 0 || apexRes.RequeueAfter < merged.RequeueAfter) {
			merged.RequeueAfter = apexRes.RequeueAfter
		}
		return merged, nil
	}

	// No CNAME yet — persist the status update here (first-reconcile path).
	if !reflect.DeepEqual(preStatus, &tunnel.Status) {
		now := metav1.Now()
		tunnel.Status.LastSyncedAt = &now
		if err := r.Status().Update(ctx, &tunnel); err != nil {
			return ctrl.Result{}, err
		}
	}

	return result, nil
}

func (r *CloudflareTunnelReconciler) reconcileTunnel(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, tunnelClient cfclient.TunnelClient, accountID string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if tunnel exists by ID
	var existing *cfclient.Tunnel
	var err error
	if tunnel.Status.TunnelID != "" {
		existing, err = tunnelClient.GetTunnel(ctx, accountID, tunnel.Status.TunnelID)
		if err != nil {
			if !cfclient.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("get tunnel by ID: %w", err)
			}
			logger.Info("tunnel not found by ID, will search by name", "tunnelID", tunnel.Status.TunnelID)
			tunnel.Status.TunnelID = ""
			existing = nil
		}
	}

	// Search by name (adopt existing)
	if existing == nil {
		tunnels, err := tunnelClient.ListTunnelsByName(ctx, accountID, tunnel.Spec.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("list tunnels: %w", err)
		}
		if len(tunnels) > 0 {
			existing = &tunnels[0]
			tunnel.Status.TunnelID = existing.ID
			logger.Info("adopted existing tunnel", "tunnelID", existing.ID)
			r.Recorder.Event(tunnel, corev1.EventTypeNormal, "TunnelAdopted",
				fmt.Sprintf("Adopted existing tunnel %s", existing.ID))
		}
	}

	requeueAfter := 30 * time.Minute
	if tunnel.Spec.Interval != nil {
		requeueAfter = tunnel.Spec.Interval.Duration
	}

	if existing == nil {
		// Generate tunnel secret only for new tunnels
		tunnelSecret, err := generateTunnelSecret()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("generate tunnel secret: %w", err)
		}

		// Create new tunnel
		created, err := tunnelClient.CreateTunnel(ctx, accountID, cfclient.TunnelParams{
			Name:         tunnel.Spec.Name,
			TunnelSecret: tunnelSecret,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create tunnel: %w", err)
		}
		tunnel.Status.TunnelID = created.ID
		logger.Info("created tunnel", "tunnelID", created.ID)
		r.Recorder.Event(tunnel, corev1.EventTypeNormal, "TunnelCreated",
			fmt.Sprintf("Created tunnel %s with ID %s", tunnel.Spec.Name, created.ID))

		// Create credentials Secret with the generated secret
		if err := r.ensureCredentialsSecret(ctx, tunnel, accountID, tunnelSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure credentials secret: %w", err)
		}
	} else {
		// For existing/adopted tunnels, ensure credentials Secret exists but don't regenerate secret
		if err := r.ensureCredentialsSecretExists(ctx, tunnel, accountID); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure credentials secret: %w", err)
		}
	}

	// Update status fields
	tunnel.Status.TunnelCNAME = fmt.Sprintf("%s.cfargotunnel.com", tunnel.Status.TunnelID)
	tunnel.Status.CredentialsSecretName = tunnel.Spec.GeneratedSecretName

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *CloudflareTunnelReconciler) ensureCredentialsSecret(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, accountID, tunnelSecret string) error {
	creds := map[string]string{
		"AccountTag":   accountID,
		"TunnelSecret": tunnelSecret,
		"TunnelID":     tunnel.Status.TunnelID,
	}
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	mutate := func(s *corev1.Secret) error {
		if err := controllerutil.SetControllerReference(tunnel, s, r.Scheme); err != nil {
			return err
		}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		for k, v := range connectorLabels(tunnel) {
			s.Labels[k] = v
		}
		if s.Data == nil {
			s.Data = make(map[string][]byte)
		}
		s.Data["credentials.json"] = credsJSON
		return nil
	}

	credSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnel.Spec.GeneratedSecretName,
			Namespace: tunnel.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, credSecret, func() error {
		return mutate(credSecret)
	})
	if err == nil {
		return nil
	}

	// Recovery: the cached client missed a pre-existing Secret (label-
	// filtered out by the manager cache, see PR #87) and the underlying
	// Create surfaced IsAlreadyExists from the API server. Re-read via
	// APIReader (uncached), re-apply the mutation, and persist via
	// Update on the cached client.
	if !errors.IsAlreadyExists(err) {
		return err
	}
	key := client.ObjectKey{Name: tunnel.Spec.GeneratedSecretName, Namespace: tunnel.Namespace}
	var fresh corev1.Secret
	if getErr := r.APIReader.Get(ctx, key, &fresh); getErr != nil {
		return fmt.Errorf("recover credentials secret after IsAlreadyExists: %w", getErr)
	}
	if mErr := mutate(&fresh); mErr != nil {
		return fmt.Errorf("mutate credentials secret after IsAlreadyExists recovery: %w", mErr)
	}
	if updErr := r.Update(ctx, &fresh); updErr != nil {
		return fmt.Errorf("update credentials secret after IsAlreadyExists recovery: %w", updErr)
	}
	return nil
}

// ensureCredentialsSecretExists ensures a credentials Secret exists for an
// adopted tunnel. Cloudflare doesn't return the original TunnelSecret on
// adoption, so the Secret is created with an empty TunnelSecret and the user
// must fill it in manually.
//
// Decision tree:
//  1. NotFound → create with empty TunnelSecret template.
//  2. Already operator-owned AND secretMatchesTunnel → no-op (steady-state).
//  3. Owned by a different controller → actionable error (wrapped AlreadyOwnedError).
//  4. Unowned/not operator-labeled → adopt (label-merge + OwnerRef, no Data touch),
//     then either return nil (data matches) or overwrite with empty template (stale data).
func (r *CloudflareTunnelReconciler) ensureCredentialsSecretExists(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, accountID string) error {
	logger := log.FromContext(ctx)

	var existing corev1.Secret
	// Use APIReader (uncached) because the manager cache filters
	// Secrets by cloudflare.io/managed=true. A pre-existing unlabeled
	// Secret would otherwise return NotFound here even though it is
	// present on the API server.
	err := r.APIReader.Get(ctx, client.ObjectKey{Name: tunnel.Spec.GeneratedSecretName, Namespace: tunnel.Namespace}, &existing)
	switch {
	case errors.IsNotFound(err):
		logger.Info("creating credentials Secret for adopted tunnel with empty TunnelSecret; user must provide TunnelSecret manually",
			"secretName", tunnel.Spec.GeneratedSecretName)
		return r.ensureCredentialsSecret(ctx, tunnel, accountID, "")
	case err != nil:
		return fmt.Errorf("get credentials secret: %w", err)
	}

	// Existing Secret found. Three branches:
	//   1. Already operator-owned and matches the tunnel    → no-op.
	//   2. Owned by a different controller                  → actionable error.
	//   3. Unowned (or only operator-owned mismatch)        → adopt, then
	//      either return (data matches) or fall through to overwrite.
	if controlledByThisTunnel(&existing, tunnel) && secretMatchesTunnel(&existing, tunnel.Status.TunnelID) {
		return nil
	}
	if otherCtrl := controlledByOther(&existing, tunnel); otherCtrl != nil {
		return fmt.Errorf(
			"credentials Secret %q is already owned by %s/%s (UID %s); "+
				"rename spec.generatedSecretName or remove the conflicting owner to allow adoption: %w",
			tunnel.Spec.GeneratedSecretName, otherCtrl.Kind, otherCtrl.Name, otherCtrl.UID,
			&controllerutil.AlreadyOwnedError{Object: &existing, Owner: *otherCtrl},
		)
	}

	// Only run adoption when we don't already own the Secret.
	// Adoption is the SetControllerReference + label-merge work; if we
	// already own it, that work is a no-op and the redundant Update
	// would just produce a spurious resource-version conflict on busy
	// clusters.
	if !controlledByThisTunnel(&existing, tunnel) {
		if err := r.adoptCredentialsSecret(ctx, tunnel, &existing); err != nil {
			return err
		}
	}

	if secretMatchesTunnel(&existing, tunnel.Status.TunnelID) {
		logger.Info("credentials Secret matches adopted tunnel; data preserved",
			"secretName", tunnel.Spec.GeneratedSecretName, "tunnelID", tunnel.Status.TunnelID)
		return nil
	}
	logger.Info("credentials Secret does not match adopted tunnel; overwriting with empty TunnelSecret, user must refill",
		"secretName", tunnel.Spec.GeneratedSecretName, "tunnelID", tunnel.Status.TunnelID)
	return r.ensureCredentialsSecret(ctx, tunnel, accountID, "")
}

// adoptCredentialsSecret takes ownership of an existing Secret without
// modifying its Data. Sets controller OwnerReference and merges
// connectorLabels into existing labels. Used for the adoption path of
// pre-existing unlabeled Secrets (see issue #90).
func (r *CloudflareTunnelReconciler) adoptCredentialsSecret(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, existing *corev1.Secret) error {
	if err := controllerutil.SetControllerReference(tunnel, existing, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on credentials secret: %w", err)
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range connectorLabels(tunnel) {
		existing.Labels[k] = v
	}
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("adopt credentials secret: %w", err)
	}
	return nil
}

// controlledByThisTunnel reports whether s has a controller
// OwnerReference whose UID matches tun.
func controlledByThisTunnel(s *corev1.Secret, tun *cloudflarev1alpha1.CloudflareTunnel) bool {
	for _, ref := range s.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller && ref.UID == tun.UID {
			return true
		}
	}
	return false
}

// controlledByOther returns the controller OwnerReference if s has a
// controller other than tun. Returns nil if s has no controller or the
// existing controller is tun itself.
func controlledByOther(s *corev1.Secret, tun *cloudflarev1alpha1.CloudflareTunnel) *metav1.OwnerReference {
	refs := s.GetOwnerReferences()
	for i := range refs {
		ref := &refs[i]
		if ref.Controller != nil && *ref.Controller && ref.UID != tun.UID {
			return ref
		}
	}
	return nil
}

// secretMatchesTunnel reports whether secret's credentials.json is parseable
// and its TunnelID field equals want.
func secretMatchesTunnel(secret *corev1.Secret, want string) bool {
	credsJSON, ok := secret.Data["credentials.json"]
	if !ok {
		return false
	}
	var creds map[string]string
	if err := json.Unmarshal(credsJSON, &creds); err != nil {
		return false
	}
	return creds["TunnelID"] == want
}

func generateTunnelSecret() (string, error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(secretBytes), nil
}

func (r *CloudflareTunnelReconciler) reconcileDelete(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	status.SetPhase(&tunnel.Status.Phase, cloudflarev1alpha1.PhaseDeleting)
	if err := r.Status().Update(ctx, tunnel); err != nil {
		logger.Error(err, "failed to update status to Deleting")
		// Continue — don't block deletion on a status-write failure.
	}

	if tunnel.Status.TunnelID != "" {
		secretNs := secretRefNamespace(tunnel.Spec.SecretRef, tunnel.Namespace)
		creds, halt, err := LoadCredentials(ctx, r.Client, r.ClientFactory,
			tunnel.Spec.SecretRef.Name, secretNs,
			r.Recorder, tunnel, &tunnel.Status.Conditions, 30*time.Second)
		if halt != nil {
			if err == nil {
				logger.V(1).Info("credential load failed during deletion; halting reconcile; remove the finalizer manually to force deletion",
					"secret", tunnel.Spec.SecretRef.Name, "namespace", secretNs)
			} else {
				logger.Error(err, "credential load failed during deletion, will retry; remove the finalizer manually to force deletion")
			}
			return *halt, err
		}
		if creds.AccountID == "" {
			err := fmt.Errorf("secret %s/%s does not contain %q key",
				secretNs, tunnel.Spec.SecretRef.Name, cfclient.SecretKeyAccountID)
			logger.Error(err, "missing Account ID during deletion, will retry; remove the finalizer manually to force deletion")
			// Phase intentionally nil: SetPhase(Deleting) at reconcileDelete entry is the
			// source of truth during deletion; derivePhase would route the deletion reason
			// to PhaseError.
			return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
				nil, cloudflarev1alpha1.ReasonSecretNotFound, wrapDeleteErr(err), 30*time.Second)
		}

		drained, derr := r.drainConnectorBeforeTunnelDelete(ctx, tunnel)
		if derr != nil {
			logger.Error(derr, "failed to drain connector before tunnel delete")
			return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
				nil, cloudflarev1alpha1.ReasonCloudflareError, wrapDeleteErr(derr), 30*time.Second)
		}
		if !drained {
			status.SetCondition(&tunnel.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady,
				metav1.ConditionFalse, cloudflarev1alpha1.ReasonDrainingConnector,
				"scaling connector Deployment to 0 before deleting tunnel from Cloudflare", tunnel.Generation)
			if err := r.Status().Update(ctx, tunnel); err != nil {
				logger.Error(err, "failed to update Draining status")
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		if err := r.tunnelClient(creds.APIToken).DeleteTunnel(ctx, creds.AccountID, tunnel.Status.TunnelID); err != nil {
			if cfclient.IsNotFound(err) {
				logger.Info("tunnel already gone in Cloudflare; treating delete as success",
					"tunnelID", tunnel.Status.TunnelID)
				// Fall through to finalizer removal — the remote object is the goal,
				// and the goal is achieved.
			} else {
				logger.Error(err, "failed to delete tunnel from Cloudflare")
				routing := ClassifyCloudflareError(err)
				requeue := routing.RequeueAfter
				// requeue==0 means either: immediate (RemoteGone, with ResetRemoteID true)
				// or "use my default" (catch-all, with ResetRemoteID false).
				if requeue == 0 && !routing.ResetRemoteID {
					requeue = 30 * time.Second
				}
				// Phase intentionally nil: SetPhase(Deleting) at reconcileDelete entry is the
				// source of truth during deletion; derivePhase would route the deletion reason
				// to PhaseError.
				return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
					nil, routing.Reason, wrapDeleteErr(err), requeue)
			}
		} else {
			logger.Info("deleted tunnel from Cloudflare", "tunnelID", tunnel.Status.TunnelID)
			r.Recorder.Event(tunnel, corev1.EventTypeNormal, "TunnelDeleted",
				fmt.Sprintf("Deleted tunnel %s from Cloudflare", tunnel.Spec.Name))
		}
	}

	controllerutil.RemoveFinalizer(tunnel, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, tunnel)
}

// drainConnectorBeforeTunnelDelete scales the operator-managed connector
// Deployment to zero and reports whether all pods have terminated. Returns:
//
//	(true,  nil)  drain complete; safe to call DeleteTunnel.
//	(false, nil)  drain in progress; caller should requeue.
//	(false, err)  Get/Update failure; caller should propagate.
//
// Gated on Spec.Connector != nil && Spec.Connector.Enabled. When the
// connector is disabled or absent, returns (true, nil) — nothing to drain.
// IsNotFound on the Deployment Get also returns (true, nil) (the user or a
// prior cleanup pass removed it; no drain needed).
func (r *CloudflareTunnelReconciler) drainConnectorBeforeTunnelDelete(
	ctx context.Context, tun *cloudflarev1alpha1.CloudflareTunnel,
) (bool, error) {
	if tun.Spec.Connector == nil || !tun.Spec.Connector.Enabled {
		return true, nil
	}
	depName := ConnectorNames(tun).Deployment
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: depName, Namespace: tun.Namespace}, &dep); err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get connector Deployment for drain: %w", err)
	}
	zero := int32(0)
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		dep.Spec.Replicas = &zero
		if err := r.Update(ctx, &dep); err != nil {
			return false, fmt.Errorf("scale connector Deployment to 0: %w", err)
		}
		return false, nil
	}
	if dep.Status.ReadyReplicas > 0 {
		return false, nil
	}
	return true, nil
}

// tunnelClient returns the test-injected TunnelClient if present, otherwise
// builds a live one from apiToken.
func (r *CloudflareTunnelReconciler) tunnelClient(apiToken string) cfclient.TunnelClient {
	if r.TunnelClientFn != nil {
		return r.TunnelClientFn(apiToken)
	}
	return cfclient.NewTunnelClientFromCF(cfclient.NewCloudflareClient(apiToken))
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareTunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareTunnel{}).
		Named("cloudflaretunnel").
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&appsv1.Deployment{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&corev1.Secret{}).
		Owns(&cloudflarev1alpha1.CloudflareDNSRecord{}).
		Watches(&cloudflarev1alpha1.CloudflareTunnelRule{}, handler.EnqueueRequestsFromMapFunc(r.mapRuleToTunnel)).
		Complete(r)
}

// mapRuleToTunnel maps a CloudflareTunnelRule watch event to the reconcile
// request for the CloudflareTunnel it references.
func (r *CloudflareTunnelReconciler) mapRuleToTunnel(ctx context.Context, obj client.Object) []ctrl.Request {
	rule, ok := obj.(*cloudflarev1alpha1.CloudflareTunnelRule)
	if !ok {
		return nil
	}
	ns := rule.Spec.TunnelRef.Namespace
	if ns == "" {
		ns = rule.Namespace
	}
	return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: rule.Spec.TunnelRef.Name}}}
}
