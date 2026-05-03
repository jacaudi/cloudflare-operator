/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
*/

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// fakeCredFactory implements the LoadCredentials factory contract by
// returning a preconfigured Credentials/error pair regardless of inputs.
type fakeCredFactory struct {
	creds cfclient.Credentials
	err   error
}

func (f *fakeCredFactory) GetCredentials(_ context.Context, _, _ string) (cfclient.Credentials, error) {
	return f.creds, f.err
}

func newTestZone() *cloudflarev1alpha1.CloudflareZone {
	return &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "z", Namespace: "ns", Generation: 1},
	}
}

func TestLoadCredentials_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cloudflarev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newTestZone()).Build()
	rec := record.NewFakeRecorder(8)

	factory := &fakeCredFactory{creds: cfclient.Credentials{APIToken: "tok"}}
	obj := newTestZone()
	var conditions []metav1.Condition

	creds, halt, err := LoadCredentials(context.Background(), c, factory,
		"my-secret", "ns", rec, obj, &conditions, time.Hour)

	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if halt != nil {
		t.Fatalf("expected halt=nil on success, got %+v", halt)
	}
	if creds.APIToken != "tok" {
		t.Errorf("expected token tok, got %s", creds.APIToken)
	}
	if len(conditions) != 0 {
		t.Errorf("expected no condition writes on success, got %+v", conditions)
	}
}

func TestLoadCredentials_NotLabeled_SetsReasonAndHalts(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cloudflarev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newTestZone()).Build()
	rec := record.NewFakeRecorder(8)

	factory := &fakeCredFactory{err: cfclient.ErrSecretNotLabeled}
	obj := newTestZone()
	var conditions []metav1.Condition

	_, halt, err := LoadCredentials(context.Background(), c, factory,
		"my-secret", "ns", rec, obj, &conditions, time.Hour)

	if err != nil {
		t.Fatalf("expected nil err (failReconcile semantics), got %v", err)
	}
	if halt == nil {
		t.Fatal("expected non-nil halt result, got nil")
	}
	if halt.RequeueAfter != time.Hour {
		t.Errorf("expected RequeueAfter=1h, got %v", halt.RequeueAfter)
	}
	if len(conditions) != 1 || conditions[0].Reason != cloudflarev1alpha1.ReasonSecretNotLabeled {
		t.Errorf("expected ReasonSecretNotLabeled, got %+v", conditions)
	}
	select {
	case got := <-rec.Events:
		if !containsSubstr(got, "SecretNotLabeled") {
			t.Errorf("expected event mentioning SecretNotLabeled, got %q", got)
		}
	default:
		t.Error("expected at least one recorder event")
	}
}

func TestLoadCredentials_NotFound_SetsReasonSecretNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cloudflarev1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(newTestZone()).Build()
	rec := record.NewFakeRecorder(8)

	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing")
	factory := &fakeCredFactory{err: fmt.Errorf("failed to get secret ns/missing: %w", notFound)}
	obj := newTestZone()
	var conditions []metav1.Condition

	_, halt, err := LoadCredentials(context.Background(), c, factory,
		"missing", "ns", rec, obj, &conditions, time.Hour)

	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if halt == nil {
		t.Fatal("expected non-nil halt result")
	}
	if len(conditions) != 1 || conditions[0].Reason != cloudflarev1alpha1.ReasonSecretNotFound {
		t.Errorf("expected ReasonSecretNotFound, got %+v", conditions)
	}
}

// containsSubstr is a tiny substring helper local to this test file (the
// package already has a slice-based `contains` defined elsewhere, so we use
// a distinct name to avoid the collision).
func containsSubstr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
