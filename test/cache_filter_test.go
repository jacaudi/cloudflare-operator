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

// Package test contains integration tests that exercise behavior the
// operator depends on from the manager runtime (cache, client, API
// reader). These tests stand up an envtest control plane and require
// the kubebuilder envtest binaries to be available via KUBEBUILDER_ASSETS.
//
// Run via `make test`, which downloads the binaries through setup-envtest
// and exports KUBEBUILDER_ASSETS automatically. Running `go test ./...`
// directly will skip these tests when KUBEBUILDER_ASSETS is unset, so
// contributors without setup-envtest installed are not blocked.
package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestSecretLabelFilter_CacheVsAPIReader stands up an envtest control
// plane, configures a manager whose cache filters Secrets by
// cloudflare.io/managed=true, then asserts that:
//
//  1. A labeled Secret IS visible via the cached client.
//  2. An unlabeled Secret is NOT visible via the cached client.
//  3. The unlabeled Secret IS visible via the manager's API reader (uncached).
//
// This is the foundational behavior the GetCredentials disambiguation
// path depends on: a cache miss on a Secret means either "doesn't exist"
// or "exists but unlabeled", and we rely on the API reader to tell them
// apart.
func TestSecretLabelFilter_CacheVsAPIReader(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest is slow; skipped in -short mode")
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test` or `setup-envtest use <ver> -p env`")
	}

	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: false,
	}
	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = te.Stop() })

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	selector, err := labels.Parse("cloudflare.io/managed=true")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Secret{}: {Label: selector},
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache failed to sync")
	}

	api := mgr.GetAPIReader()
	cached := mgr.GetClient()

	// Use a non-cached client for setup writes so we don't race the cache.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("direct client: %v", err)
	}

	labeled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "labeled",
			Namespace: "default",
			Labels:    map[string]string{"cloudflare.io/managed": "true"},
		},
	}
	unlabeled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "unlabeled", Namespace: "default"},
	}
	if err := directClient.Create(ctx, labeled); err != nil {
		t.Fatalf("create labeled: %v", err)
	}
	if err := directClient.Create(ctx, unlabeled); err != nil {
		t.Fatalf("create unlabeled: %v", err)
	}

	// Give the cache a beat to observe the labeled write. Polling on the
	// labeled Secret is sufficient to serialize against the unlabeled
	// observation too: both writes share the same client, so the apiserver
	// assigns monotonically-increasing resourceVersions and the informer
	// observes them in order. Once the labeled Secret is visible, the
	// earlier unlabeled write has already been processed (and filtered out)
	// by the same informer.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var probe corev1.Secret
		if err := cached.Get(ctx, client.ObjectKey{Name: "labeled", Namespace: "default"}, &probe); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 1. Labeled visible via cached client.
	var gotLabeled corev1.Secret
	if err := cached.Get(ctx, client.ObjectKey{Name: "labeled", Namespace: "default"}, &gotLabeled); err != nil {
		t.Errorf("cached Get(labeled) unexpected error: %v", err)
	}

	// 2. Unlabeled NOT visible via cached client (filtered by selector,
	//    so it surfaces as IsNotFound).
	var gotUnlabeled corev1.Secret
	err = cached.Get(ctx, client.ObjectKey{Name: "unlabeled", Namespace: "default"}, &gotUnlabeled)
	if !apierrors.IsNotFound(err) {
		t.Errorf("cached Get(unlabeled) expected IsNotFound, got %v", err)
	}

	// 3. Unlabeled IS visible via API reader (uncached path).
	var probeUnlabeled corev1.Secret
	if err := api.Get(ctx, client.ObjectKey{Name: "unlabeled", Namespace: "default"}, &probeUnlabeled); err != nil {
		t.Errorf("apiReader Get(unlabeled) unexpected error: %v", err)
	}
}
