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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// CloudflareTunnelReconciler reconciles a CloudflareTunnel object
type CloudflareTunnelReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	ClientFactory  *cfclient.ClientFactory
	TunnelClientFn func(apiToken string) cfclient.TunnelClient
}

// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile moves the current state of the cluster closer to the desired state
// for a CloudflareTunnel resource. It handles the full lifecycle of tunnels
// including creation, adoption of existing tunnels, credential Secret
// generation, and deletion.
func (r *CloudflareTunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the CR
	var tunnel cloudflarev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, req.NamespacedName, &tunnel); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

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
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Get API token
	apiToken, err := r.ClientFactory.GetAPIToken(ctx, tunnel.Spec.SecretRef.Name, tunnel.Namespace)
	if err != nil {
		log.Error(err, "failed to get API token")
		status.SetReady(&tunnel.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonSecretNotFound, err.Error(), tunnel.Generation)
		if statusErr := r.Status().Update(ctx, &tunnel); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Build tunnel client
	var tunnelClient cfclient.TunnelClient
	if r.TunnelClientFn != nil {
		tunnelClient = r.TunnelClientFn(apiToken)
	} else {
		cfClient := cfclient.NewCloudflareClient(apiToken)
		tunnelClient = cfclient.NewTunnelClientFromCF(cfClient)
	}

	// 6. Reconcile the tunnel
	result, err := r.reconcileTunnel(ctx, &tunnel, tunnelClient)
	if err != nil {
		log.Error(err, "reconciliation failed")
		status.SetReady(&tunnel.Status.Conditions, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonCloudflareError, err.Error(), tunnel.Generation)
		r.Recorder.Event(&tunnel, "Warning", "SyncFailed", err.Error())
		if statusErr := r.Status().Update(ctx, &tunnel); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// 7. Update status
	tunnel.Status.ObservedGeneration = tunnel.Generation
	now := metav1.Now()
	tunnel.Status.LastSyncedAt = &now
	status.SetReady(&tunnel.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Tunnel synced", tunnel.Generation)
	status.SetSynced(&tunnel.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Tunnel synced", tunnel.Generation)
	if err := r.Status().Update(ctx, &tunnel); err != nil {
		return ctrl.Result{}, err
	}

	return result, nil
}

func (r *CloudflareTunnelReconciler) reconcileTunnel(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, tunnelClient cfclient.TunnelClient) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Check if tunnel exists by ID
	var existing *cfclient.Tunnel
	var err error
	if tunnel.Status.TunnelID != "" {
		existing, err = tunnelClient.GetTunnel(ctx, tunnel.Spec.AccountID, tunnel.Status.TunnelID)
		if err != nil {
			log.Info("could not fetch tunnel by ID, will search by name", "tunnelID", tunnel.Status.TunnelID)
			tunnel.Status.TunnelID = ""
			existing = nil
		}
	}

	// Search by name (adopt existing)
	if existing == nil {
		tunnels, err := tunnelClient.ListTunnelsByName(ctx, tunnel.Spec.AccountID, tunnel.Spec.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("list tunnels: %w", err)
		}
		if len(tunnels) > 0 {
			existing = &tunnels[0]
			tunnel.Status.TunnelID = existing.ID
			log.Info("adopted existing tunnel", "tunnelID", existing.ID)
			r.Recorder.Event(tunnel, "Normal", "TunnelAdopted",
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
		created, err := tunnelClient.CreateTunnel(ctx, tunnel.Spec.AccountID, cfclient.TunnelParams{
			Name:         tunnel.Spec.Name,
			TunnelSecret: tunnelSecret,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("create tunnel: %w", err)
		}
		tunnel.Status.TunnelID = created.ID
		log.Info("created tunnel", "tunnelID", created.ID)
		r.Recorder.Event(tunnel, "Normal", "TunnelCreated",
			fmt.Sprintf("Created tunnel %s with ID %s", tunnel.Spec.Name, created.ID))

		// Create credentials Secret with the generated secret
		if err := r.ensureCredentialsSecret(ctx, tunnel, tunnelSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure credentials secret: %w", err)
		}
	} else {
		// For existing/adopted tunnels, ensure credentials Secret exists but don't regenerate secret
		if err := r.ensureCredentialsSecretExists(ctx, tunnel); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure credentials secret: %w", err)
		}
	}

	// Update status fields
	tunnel.Status.TunnelCNAME = fmt.Sprintf("%s.cfargotunnel.com", tunnel.Status.TunnelID)
	tunnel.Status.CredentialsSecretName = tunnel.Spec.GeneratedSecretName

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *CloudflareTunnelReconciler) ensureCredentialsSecret(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, tunnelSecret string) error {
	creds := map[string]string{
		"AccountTag":   tunnel.Spec.AccountID,
		"TunnelSecret": tunnelSecret,
		"TunnelID":     tunnel.Status.TunnelID,
	}
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	credSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnel.Spec.GeneratedSecretName,
			Namespace: tunnel.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, credSecret, func() error {
		if err := controllerutil.SetControllerReference(tunnel, credSecret, r.Scheme); err != nil {
			return err
		}
		if credSecret.Data == nil {
			credSecret.Data = make(map[string][]byte)
		}
		credSecret.Data["credentials.json"] = credsJSON
		return nil
	})
	return err
}

// ensureCredentialsSecretExists checks if the credentials Secret already exists
// with the correct TunnelID. If it does, no changes are made. If it doesn't exist,
// it is created with an empty TunnelSecret (for adopted tunnels the original secret
// is unavailable; the user must provide it manually).
func (r *CloudflareTunnelReconciler) ensureCredentialsSecretExists(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel) error {
	log := log.FromContext(ctx)

	var existingSecret corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Name: tunnel.Spec.GeneratedSecretName, Namespace: tunnel.Namespace}, &existingSecret)
	if err == nil {
		// Secret exists — check if TunnelID matches
		if credsJSON, ok := existingSecret.Data["credentials.json"]; ok {
			var creds map[string]string
			if jsonErr := json.Unmarshal(credsJSON, &creds); jsonErr == nil {
				if creds["TunnelID"] == tunnel.Status.TunnelID {
					return nil // Secret exists with correct TunnelID, nothing to do
				}
			}
		}
	}

	if !errors.IsNotFound(err) && err != nil {
		return fmt.Errorf("get credentials secret: %w", err)
	}

	// Secret doesn't exist or has wrong TunnelID — create with empty TunnelSecret
	log.Info("creating credentials secret for adopted tunnel with empty TunnelSecret; user must provide TunnelSecret manually",
		"secretName", tunnel.Spec.GeneratedSecretName)
	return r.ensureCredentialsSecret(ctx, tunnel, "")
}

func generateTunnelSecret() (string, error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(secretBytes), nil
}

func (r *CloudflareTunnelReconciler) reconcileDelete(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if tunnel.Status.TunnelID != "" {
		apiToken, err := r.ClientFactory.GetAPIToken(ctx, tunnel.Spec.SecretRef.Name, tunnel.Namespace)
		if err != nil {
			log.Error(err, "failed to get API token during deletion, will retry; remove the finalizer manually to force deletion")
			status.SetReady(&tunnel.Status.Conditions, metav1.ConditionFalse,
				cloudflarev1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Cannot delete Cloudflare resource: %v. Remove the finalizer manually to force deletion.", err),
				tunnel.Generation)
			if statusErr := r.Status().Update(ctx, tunnel); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		} else {
			var tunnelClient cfclient.TunnelClient
			if r.TunnelClientFn != nil {
				tunnelClient = r.TunnelClientFn(apiToken)
			} else {
				cfClient := cfclient.NewCloudflareClient(apiToken)
				tunnelClient = cfclient.NewTunnelClientFromCF(cfClient)
			}

			if err := tunnelClient.DeleteTunnel(ctx, tunnel.Spec.AccountID, tunnel.Status.TunnelID); err != nil {
				log.Error(err, "failed to delete tunnel from Cloudflare")
				status.SetReady(&tunnel.Status.Conditions, metav1.ConditionFalse,
					cloudflarev1alpha1.ReasonCloudflareError, err.Error(), tunnel.Generation)
				if statusErr := r.Status().Update(ctx, tunnel); statusErr != nil {
					log.Error(statusErr, "failed to update status")
				}
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			log.Info("deleted tunnel from Cloudflare", "tunnelID", tunnel.Status.TunnelID)
			r.Recorder.Event(tunnel, "Normal", "TunnelDeleted",
				fmt.Sprintf("Deleted tunnel %s from Cloudflare", tunnel.Spec.Name))
		}
	}

	controllerutil.RemoveFinalizer(tunnel, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, tunnel)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareTunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudflarev1alpha1.CloudflareTunnel{}).
		Named("cloudflaretunnel").
		Complete(r)
}
