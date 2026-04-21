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
	creds, err := r.ClientFactory.GetCredentials(ctx, tunnel.Spec.SecretRef.Name, tunnel.Namespace)
	if err != nil {
		logger.Error(err, "failed to get credentials")
		return failReconcile(ctx, r.Client, &tunnel, &tunnel.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}
	if creds.AccountID == "" {
		err := fmt.Errorf("secret %s/%s does not contain %q key",
			tunnel.Namespace, tunnel.Spec.SecretRef.Name, cfclient.SecretKeyAccountID)
		logger.Error(err, "missing Account ID")
		return failReconcile(ctx, r.Client, &tunnel, &tunnel.Status.Conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, 30*time.Second)
	}

	// 5. Reconcile the tunnel
	result, err := r.reconcileTunnel(ctx, &tunnel, r.tunnelClient(creds.APIToken), creds.AccountID)
	if err != nil {
		logger.Error(err, "reconciliation failed")
		r.Recorder.Event(&tunnel, corev1.EventTypeWarning, "SyncFailed", err.Error())
		return failReconcile(ctx, r.Client, &tunnel, &tunnel.Status.Conditions,
			cloudflarev1alpha1.ReasonCloudflareError, err, time.Minute)
	}

	// 7. Persist status only if anything materially changed.
	tunnel.Status.ObservedGeneration = tunnel.Generation
	status.SetReady(&tunnel.Status.Conditions, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "Tunnel synced", tunnel.Generation)
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
			logger.Info("could not fetch tunnel by ID, will search by name", "tunnelID", tunnel.Status.TunnelID)
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

// ensureCredentialsSecretExists ensures a credentials Secret exists for an
// adopted tunnel. Cloudflare doesn't return the original TunnelSecret on
// adoption, so the Secret is created with an empty TunnelSecret and the user
// must fill it in manually. If a Secret already exists with a matching TunnelID
// it is preserved; otherwise it is (over)written with the empty template.
func (r *CloudflareTunnelReconciler) ensureCredentialsSecretExists(ctx context.Context, tunnel *cloudflarev1alpha1.CloudflareTunnel, accountID string) error {
	logger := log.FromContext(ctx)

	var existing corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Name: tunnel.Spec.GeneratedSecretName, Namespace: tunnel.Namespace}, &existing)
	switch {
	case errors.IsNotFound(err):
		logger.Info("creating credentials Secret for adopted tunnel with empty TunnelSecret; user must provide TunnelSecret manually",
			"secretName", tunnel.Spec.GeneratedSecretName)
	case err != nil:
		return fmt.Errorf("get credentials secret: %w", err)
	case secretMatchesTunnel(&existing, tunnel.Status.TunnelID):
		return nil
	default:
		logger.Info("credentials Secret does not match adopted tunnel; overwriting with empty TunnelSecret, user must refill",
			"secretName", tunnel.Spec.GeneratedSecretName, "tunnelID", tunnel.Status.TunnelID)
	}

	return r.ensureCredentialsSecret(ctx, tunnel, accountID, "")
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

	if tunnel.Status.TunnelID != "" {
		creds, err := r.ClientFactory.GetCredentials(ctx, tunnel.Spec.SecretRef.Name, tunnel.Namespace)
		if err != nil {
			logger.Error(err, "failed to get credentials during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
				cloudflarev1alpha1.ReasonSecretNotFound, wrapDeleteErr(err), 30*time.Second)
		}
		if creds.AccountID == "" {
			err := fmt.Errorf("secret %s/%s does not contain %q key",
				tunnel.Namespace, tunnel.Spec.SecretRef.Name, cfclient.SecretKeyAccountID)
			logger.Error(err, "missing Account ID during deletion, will retry; remove the finalizer manually to force deletion")
			return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
				cloudflarev1alpha1.ReasonSecretNotFound, wrapDeleteErr(err), 30*time.Second)
		}

		if err := r.tunnelClient(creds.APIToken).DeleteTunnel(ctx, creds.AccountID, tunnel.Status.TunnelID); err != nil {
			logger.Error(err, "failed to delete tunnel from Cloudflare")
			return failReconcile(ctx, r.Client, tunnel, &tunnel.Status.Conditions,
				cloudflarev1alpha1.ReasonCloudflareError, err, 30*time.Second)
		}
		logger.Info("deleted tunnel from Cloudflare", "tunnelID", tunnel.Status.TunnelID)
		r.Recorder.Event(tunnel, corev1.EventTypeNormal, "TunnelDeleted",
			fmt.Sprintf("Deleted tunnel %s from Cloudflare", tunnel.Spec.Name))
	}

	controllerutil.RemoveFinalizer(tunnel, cloudflarev1alpha1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, tunnel)
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
		Complete(r)
}
