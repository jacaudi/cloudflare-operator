# Source-Controller Orphan DNS Record GC — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the GC gap in source-controllers (HTTPRoute, TLSRoute, Gateway, Service) so that derived `CloudflareDNSRecord` CRs are deleted when the source's current state stops requesting them (annotation removed, parent unset, listener hostnames cleared) — fixing [issue #145](https://github.com/jacaudi/cloudflare-operator/issues/145).

**Architecture:** Five new `pruneOrphanedDNSRecords(..., desired=nil)` calls inserted at the definitive-deactivation early-return branches in the four source-controllers. Reuses the existing pruner (`internal/controller/tunnel/orphan_prune.go`); no new abstractions, no signature changes. Each branch passes `nil` as `desired`, which the pruner reads as "nothing desired" — so every CR with matching source-identity labels in the source's namespace is deleted (and its finalizer handles the Cloudflare-side cleanup). Transient and ambiguous branches deliberately untouched.

**Tech Stack:** Go 1.26, controller-runtime, sigs.k8s.io/gateway-api v1.5, `internal/controller/tunnel` package, `stretchr/testify/require` for assertions, `sigs.k8s.io/controller-runtime/pkg/client/fake` for the test client.

> **For Claude:** REQUIRED EXECUTION WORKFLOW (follow in order):
> 1. `superpowers:using-git-worktrees` — Isolate work in a dedicated worktree
> 2. `superpowers:subagent-driven-development` — Dispatch a fresh subagent per task
> 3. `superpowers:test-driven-development` — All subagents use TDD
> 4. `superpowers:verification-before-completion` — Verify all tests pass per task
> 5. `superpowers:requesting-code-review` — Code review after each task (built in)
> 6. After all tasks: comprehensive code review on full diff from branch point (automatic)
> 7. `superpowers:finishing-a-development-branch` — Complete the branch
>
> Skills carry their own model and effort settings. Do not override them.

**Design reference:** [`docs/plans/2026-05-28-source-controller-orphan-gc-design.md`](2026-05-28-source-controller-orphan-gc-design.md)

---

## Files Touched

| File | Role |
|---|---|
| `internal/controller/tunnel/httproute_source_controller.go` | Add 1 prune call at `parent == nil` branch |
| `internal/controller/tunnel/tlsroute_source_controller.go` | Add 1 prune call at `parent == nil` branch |
| `internal/controller/tunnel/gateway_source_controller.go` | Add 2 prune calls (`!enabled` and `len(hostnames) == 0`) |
| `internal/controller/tunnel/service_source_controller.go` | Add 1 prune call at `!enabled` branch |
| `internal/controller/tunnel/httproute_source_controller_test.go` | Add 1 test: `TestHTTPRouteSource_ParentDeactivation_PrunesEmittedCRs` |
| `internal/controller/tunnel/tlsroute_source_controller_test.go` | Add 1 test: `TestTLSRouteSource_ParentDeactivation_PrunesEmittedCRs` |
| `internal/controller/tunnel/gateway_source_controller_test.go` | Add 2 tests: `TestGatewaySource_OptOut_PrunesEmittedCRs` (primary repro), `TestGatewaySource_HostnamesCleared_PrunesEmittedCRs` |
| `internal/controller/tunnel/service_source_controller_test.go` | Add 1 test: `TestServiceSource_OptOut_PrunesEmittedCRs` |

No imports change (the `pruneOrphanedDNSRecords` helper lives in the same package).

---

## Common Pattern

Every new prune call uses the **exact** snippet below, with `<KIND>`, `<NAME>`, and `<NS>` substituted per call site. The call goes **after** the existing `r.Cache.Clear(prev, srcKey)` (or equivalent cache-sweep step) and **before** the existing `return reconcile.Result{}, nil`.

```go
// Deactivation prune: source no longer requests any DNS records.
// Delete previously-emitted CRs labelled with this source's identity so
// they don't squat on Cloudflare-side records and block competing CRs.
// Best-effort: log-and-continue on error — the controller retries on the
// next reconcile and any surviving orphan is picked up then.
pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "<KIND>", <NAME>, <NS>, nil)
if perr != nil {
    logger.Error(perr, "orphan-prune failed during deactivation sweep")
} else if len(pruned) > 0 {
    r.dedupe.emit(r.Recorder, <SOURCE_OBJECT>, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
        fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
}
```

**Why the event uses the existing `ReasonOrphanedDNSRecordPruned` constant:** the semantic is identical — "we pruned CRs that this source previously emitted." A distinct reason would split an observability stream that operators are already filtering on; the message text disambiguates the two call sites.

**Service-controller note:** Service's existing happy-path prune call uses `r.dedupe.emit(r.Recorder, &svc, ...)`. Gateway's existing prune-event style at `!enabled` and `len(hostnames)==0` branches must match each controller's local conventions for the recorder field — see each task for exact code.

---

## Task 1: HTTPRoute — Prune at `parent == nil`

**Files:**
- Modify: `internal/controller/tunnel/httproute_source_controller.go:123-129`
- Test: `internal/controller/tunnel/httproute_source_controller_test.go` (append new test)

- [ ] **Step 1.1: Write the failing test**

Append to `internal/controller/tunnel/httproute_source_controller_test.go`:

```go
// TestHTTPRouteSource_ParentDeactivation_PrunesEmittedCRs verifies issue #145
// fix: when an HTTPRoute's tunnel-targeted parent disappears (parent Gateway
// loses its cloudflare.io/tunnel annotation), the previously-emitted
// CloudflareDNSRecord CRs are deleted on the next reconcile.
func TestHTTPRouteSource_ParentDeactivation_PrunesEmittedCRs(t *testing.T) {
    // Pass 1 fixtures: tunnel-targeted Gateway + HTTPRoute pointing at it.
    gw := mkGw("gw", "gw-ns", "gw-ns/envoy-gw", []string{"app.example.com"})
    svc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
        Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
    }
    rt := &gwv1.HTTPRoute{
        ObjectMeta: metav1.ObjectMeta{Name: "rt", Namespace: "rt-ns"},
        Spec: gwv1.HTTPRouteSpec{
            CommonRouteSpec: gwv1.CommonRouteSpec{
                ParentRefs: []gwv1.ParentReference{{
                    Name:      "gw",
                    Namespace: ptrNs("gw-ns"),
                }},
            },
            Hostnames: []gwv1.Hostname{"app.example.com"},
        },
    }
    preTun := gwPreCreatedTunnel("gw-ns-edge", "gw-ns")
    base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun, rt).
        WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
    c := reconcilelib.SSATranslatingClient(t, base)

    cache := tunnelsynth.NewCache()
    r := &HTTPRouteSourceReconciler{
        Client: c, Scheme: gwScheme(t), Cache: cache,
        DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
    }

    // Pass 1: reconcile while the parent is tunnel-targeted → expect 1 CR.
    _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "rt-ns", Name: "rt"}})
    require.NoError(t, err)
    var dnsList v2alpha1.CloudflareDNSRecordList
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")
    require.Equal(t, "HTTPRoute", dnsList.Items[0].Labels[conventions.LabelSourceKind])

    // Mutate: strip the tunnel annotation off the Gateway → next reconcile
    // finds no tunnel-targeted parent → parent == nil branch.
    var got gwv1.Gateway
    require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw"}, &got))
    delete(got.Annotations, conventions.AnnotationTunnel)
    require.NoError(t, c.Update(context.Background(), &got))

    // Pass 2: reconcile → expect CR deleted by deactivation prune.
    _, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "rt-ns", Name: "rt"}})
    require.NoError(t, err)
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Empty(t, dnsList.Items, "deactivation prune should delete the previously-emitted CR")
}
```

`ptrNs` is the existing helper in `httproute_source_controller_test.go:385` — already in scope for this test.

- [ ] **Step 1.2: Run the test to verify it fails**

```bash
go test ./internal/controller/tunnel/ -run TestHTTPRouteSource_ParentDeactivation_PrunesEmittedCRs -v
```

Expected: FAIL. The assertion `require.Empty(t, dnsList.Items, ...)` fails because the CR persists across reconciles (the bug).

- [ ] **Step 1.3: Add the prune call**

Edit `internal/controller/tunnel/httproute_source_controller.go` at the `parent == nil` branch (currently lines 123–129):

Replace this block:
```go
if parent == nil {
    // Sweep any prior attachment — the Route was previously tunnel-
    // targeted, then its parents changed. Don't leak cache entries.
    if prev, ok := r.tracker.sweep(srcKey); ok {
        r.Cache.Clear(prev, srcKey)
    }
    return reconcile.Result{}, nil
}
```

With:
```go
if parent == nil {
    // Sweep any prior attachment — the Route was previously tunnel-
    // targeted, then its parents changed. Don't leak cache entries.
    if prev, ok := r.tracker.sweep(srcKey); ok {
        r.Cache.Clear(prev, srcKey)
    }
    // Deactivation prune (issue #145): source no longer requests any DNS
    // records. Delete previously-emitted CRs so they don't squat on
    // Cloudflare-side records. Best-effort: log and continue.
    pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "HTTPRoute", rt.Name, rt.Namespace, nil)
    if perr != nil {
        logger.Error(perr, "orphan-prune failed during deactivation sweep")
    } else if len(pruned) > 0 {
        r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
            fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
    }
    return reconcile.Result{}, nil
}
```

- [ ] **Step 1.4: Run the test to verify it passes**

```bash
go test ./internal/controller/tunnel/ -run TestHTTPRouteSource_ParentDeactivation_PrunesEmittedCRs -v
```

Expected: PASS.

- [ ] **Step 1.5: Run the full package test suite to confirm no regressions**

```bash
go test ./internal/controller/tunnel/...
```

Expected: PASS.

- [ ] **Step 1.6: Commit**

```bash
git add internal/controller/tunnel/httproute_source_controller.go internal/controller/tunnel/httproute_source_controller_test.go
git commit -m "$(cat <<'EOF'
fix(httproute): prune emitted DNSRecord CRs when parent stops being tunnel-targeted

When an HTTPRoute's tunnel-targeted parent loses its cloudflare.io/tunnel
annotation, findTunnelTargetedParent returns nil and the reconcile early-
returns. The end-of-reconcile pruneOrphanedDNSRecords call is never
reached, so previously-emitted CloudflareDNSRecord CRs persist as
orphans and continue to hold Cloudflare-side records. Add an explicit
prune at the parent==nil branch with desired=nil so all CRs labelled
with this source's identity are deleted.

Refs issue #145.
EOF
)"
```

---

## Task 2: TLSRoute — Prune at `parent == nil`

**Files:**
- Modify: `internal/controller/tunnel/tlsroute_source_controller.go:121-125`
- Test: `internal/controller/tunnel/tlsroute_source_controller_test.go` (append new test)

- [ ] **Step 2.1: Write the failing test**

Append to `internal/controller/tunnel/tlsroute_source_controller_test.go` (assumes the file already has helpers analogous to `mkGw`, `gwPreCreatedTunnel`, etc.; if helper names differ, follow the file's existing conventions):

```go
// TestTLSRouteSource_ParentDeactivation_PrunesEmittedCRs verifies issue #145
// fix: when a TLSRoute's tunnel-targeted parent disappears (parent Gateway
// loses its cloudflare.io/tunnel annotation), the previously-emitted
// CloudflareDNSRecord CRs are deleted on the next reconcile.
func TestTLSRouteSource_ParentDeactivation_PrunesEmittedCRs(t *testing.T) {
    // Build a TLS listener on the Gateway with a hostname (apex).
    hp := gwv1.Hostname("apex.example.com")
    gw := &gwv1.Gateway{
        ObjectMeta: metav1.ObjectMeta{
            Name: "gw", Namespace: "gw-ns",
            Annotations: map[string]string{
                conventions.AnnotationTunnel:         "true",
                conventions.AnnotationTunnelName:     "edge",
                conventions.AnnotationGatewayService: "gw-ns/envoy-gw",
            },
        },
        Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{
            {Name: "tls", Hostname: &hp, Port: 443, Protocol: gwv1.TLSProtocolType},
        }},
    }
    svc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
        Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
    }
    rt := &gwv1a2.TLSRoute{
        ObjectMeta: metav1.ObjectMeta{Name: "rt", Namespace: "rt-ns"},
        Spec: gwv1a2.TLSRouteSpec{
            CommonRouteSpec: gwv1.CommonRouteSpec{
                ParentRefs: []gwv1.ParentReference{{
                    Name:      "gw",
                    Namespace: ptrNs("gw-ns"),
                }},
            },
            Hostnames: []gwv1.Hostname{"apex.example.com"},
        },
    }
    preTun := gwPreCreatedTunnel("gw-ns-edge", "gw-ns")
    base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, svc, preTun, rt).
        WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
    c := reconcilelib.SSATranslatingClient(t, base)

    cache := tunnelsynth.NewCache()
    r := &TLSRouteSourceReconciler{
        Client: c, Scheme: tlsRtScheme(t), Cache: cache,
        DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
    }

    // Pass 1: expect 1 CR emitted.
    _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "rt-ns", Name: "rt"}})
    require.NoError(t, err)
    var dnsList v2alpha1.CloudflareDNSRecordList
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")
    require.Equal(t, "TLSRoute", dnsList.Items[0].Labels[conventions.LabelSourceKind])

    // Mutate: strip the tunnel annotation off the Gateway → parent == nil.
    var got gwv1.Gateway
    require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw"}, &got))
    delete(got.Annotations, conventions.AnnotationTunnel)
    require.NoError(t, c.Update(context.Background(), &got))

    // Pass 2: expect CR deleted.
    _, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "rt-ns", Name: "rt"}})
    require.NoError(t, err)
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Empty(t, dnsList.Items, "deactivation prune should delete the previously-emitted CR")
}
```

`tlsRtScheme(t)` is the existing scheme helper in `tlsroute_source_controller_test.go:36`. `ptrNs` is the existing `*gwv1.Namespace` helper from `httproute_source_controller_test.go:385` (in the same package).

- [ ] **Step 2.2: Run the test to verify it fails**

```bash
go test ./internal/controller/tunnel/ -run TestTLSRouteSource_ParentDeactivation_PrunesEmittedCRs -v
```

Expected: FAIL on the `require.Empty(t, dnsList.Items, ...)` assertion.

- [ ] **Step 2.3: Add the prune call**

Edit `internal/controller/tunnel/tlsroute_source_controller.go` at the `parent == nil` branch (currently lines 121–125):

Replace:
```go
if parent == nil {
    if prev, ok := r.tracker.sweep(srcKey); ok {
        r.Cache.Clear(prev, srcKey)
    }
    return reconcile.Result{}, nil
}
```

With:
```go
if parent == nil {
    if prev, ok := r.tracker.sweep(srcKey); ok {
        r.Cache.Clear(prev, srcKey)
    }
    // Deactivation prune (issue #145): source no longer requests any DNS
    // records. Delete previously-emitted CRs so they don't squat on
    // Cloudflare-side records. Best-effort: log and continue.
    pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "TLSRoute", rt.Name, rt.Namespace, nil)
    if perr != nil {
        logger.Error(perr, "orphan-prune failed during deactivation sweep")
    } else if len(pruned) > 0 {
        r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
            fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
    }
    return reconcile.Result{}, nil
}
```

- [ ] **Step 2.4: Run the test to verify it passes**

```bash
go test ./internal/controller/tunnel/ -run TestTLSRouteSource_ParentDeactivation_PrunesEmittedCRs -v
```

Expected: PASS.

- [ ] **Step 2.5: Run the full package test suite**

```bash
go test ./internal/controller/tunnel/...
```

Expected: PASS.

- [ ] **Step 2.6: Commit**

```bash
git add internal/controller/tunnel/tlsroute_source_controller.go internal/controller/tunnel/tlsroute_source_controller_test.go
git commit -m "$(cat <<'EOF'
fix(tlsroute): prune emitted DNSRecord CRs when parent stops being tunnel-targeted

Mirrors the HTTPRoute fix in the prior commit: TLSRoute's parent==nil
branch was missing the deactivation prune. Add it so previously-emitted
CRs are deleted when the parent Gateway loses its cloudflare.io/tunnel
annotation.

Refs issue #145.
EOF
)"
```

---

## Task 3: Gateway — Prune at `!enabled` (primary repro) and `len(hostnames) == 0`

**Files:**
- Modify: `internal/controller/tunnel/gateway_source_controller.go` (lines 110–130 and 135–141)
- Test: `internal/controller/tunnel/gateway_source_controller_test.go` (append two tests)

Two prune calls land in the same file in this task because the two Gateway deactivation branches are adjacent and share idiom; reviewing them together avoids two passes through the same file.

- [ ] **Step 3.1: Write the failing test for `!enabled` (primary repro for issue #145)**

Append to `internal/controller/tunnel/gateway_source_controller_test.go`:

```go
// TestGatewaySource_OptOut_PrunesEmittedCRs is the primary repro for issue
// #145: a Gateway with cloudflare.io/tunnel="true" emits DNSRecord CRs;
// when the annotation is removed, the previously-emitted CRs must be
// deleted on the next reconcile.
func TestGatewaySource_OptOut_PrunesEmittedCRs(t *testing.T) {
    gw := mkGw("gw", "gw-ns", "gw-ns/envoy-gw", []string{"apex.example.com"})
    svc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
        Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
    }
    preTun := gwPreCreatedTunnel("gw-ns-edge", "gw-ns")
    base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
        WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
    c := reconcilelib.SSATranslatingClient(t, base)

    r := &GatewaySourceReconciler{
        Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(),
        DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
    }

    // Pass 1: enabled → CR emitted.
    _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
    require.NoError(t, err)
    var dnsList v2alpha1.CloudflareDNSRecordList
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")
    require.Equal(t, "Gateway", dnsList.Items[0].Labels[conventions.LabelSourceKind])

    // Mutate: remove cloudflare.io/tunnel → !enabled branch on next reconcile.
    var got gwv1.Gateway
    require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw"}, &got))
    delete(got.Annotations, conventions.AnnotationTunnel)
    require.NoError(t, c.Update(context.Background(), &got))

    // Pass 2: !enabled → expect CR deleted.
    _, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
    require.NoError(t, err)
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Empty(t, dnsList.Items, "opt-out prune should delete the previously-emitted CR")
}
```

- [ ] **Step 3.2: Write the failing test for `len(hostnames) == 0`**

Append to the same test file:

```go
// TestGatewaySource_HostnamesCleared_PrunesEmittedCRs covers the second
// Gateway deactivation branch: an opted-in Gateway whose listeners all
// lose their hostname must prune previously-emitted CRs.
func TestGatewaySource_HostnamesCleared_PrunesEmittedCRs(t *testing.T) {
    gw := mkGw("gw", "gw-ns", "gw-ns/envoy-gw", []string{"apex.example.com"})
    svc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
        Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
    }
    preTun := gwPreCreatedTunnel("gw-ns-edge", "gw-ns")
    base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
        WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
    c := reconcilelib.SSATranslatingClient(t, base)

    r := &GatewaySourceReconciler{
        Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(), Recorder: record.NewFakeRecorder(8),
        DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
    }

    // Pass 1: 1 CR emitted.
    _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
    require.NoError(t, err)
    var dnsList v2alpha1.CloudflareDNSRecordList
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")

    // Mutate: clear all listener hostnames (set to nil pointer).
    var got gwv1.Gateway
    require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "gw"}, &got))
    for i := range got.Spec.Listeners {
        got.Spec.Listeners[i].Hostname = nil
    }
    require.NoError(t, c.Update(context.Background(), &got))

    // Pass 2: len(hostnames) == 0 → expect CR deleted.
    _, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
    require.NoError(t, err)
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Empty(t, dnsList.Items, "no-hostnames prune should delete the previously-emitted CR")
}
```

- [ ] **Step 3.3: Run both tests to verify they fail**

```bash
go test ./internal/controller/tunnel/ -run 'TestGatewaySource_(OptOut|HostnamesCleared)_PrunesEmittedCRs' -v
```

Expected: both FAIL on the `require.Empty(...)` assertions.

- [ ] **Step 3.4: Add the prune call at `!enabled`**

Edit `internal/controller/tunnel/gateway_source_controller.go` at the `!enabled` branch (currently lines 110–130). Inside the existing `if !enabled { ... }` block, insert the prune call **after** the `r.Cache.Clear(...)` call for the derived key and **before** `return reconcile.Result{}, nil`:

```go
        if k, derr := DeriveTunnelName(gw.Namespace, gw.Annotations[conventions.AnnotationTunnelName]); derr == nil {
            r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: gw.Namespace, Name: k}, srcKey)
        }
        // Deactivation prune (issue #145): source opted out of the tunnel.
        // Delete previously-emitted CRs so they don't squat on Cloudflare-
        // side records. Best-effort: log and continue.
        pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Gateway", gw.Name, gw.Namespace, nil)
        if perr != nil {
            logger.Error(perr, "orphan-prune failed during deactivation sweep")
        } else if len(pruned) > 0 {
            r.dedupe.emit(r.recorder, &gw, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
                fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
        }
        return reconcile.Result{}, nil
    }
```

Note: this branch uses `r.recorder` (lowercase, the dedupe-wrapped recorder) — match the existing event-emission style at line 136 in the same file.

- [ ] **Step 3.5: Add the prune call at `len(hostnames) == 0`**

Same file, the `len(hostnames) == 0` branch (currently lines 135–141). Insert after `r.Cache.Clear(prev, srcKey)`:

```go
    if len(hostnames) == 0 {
        r.dedupe.emit(r.recorder, &gw, corev1.EventTypeWarning, conventions.ReasonNoListenerHostname,
            "Gateway has no listener with a hostname; tunnel-apex synthesis requires at least one")
        if prev, ok := r.tracker.sweep(srcKey); ok {
            r.Cache.Clear(prev, srcKey)
        }
        // Deactivation prune (issue #145): no listener hostnames means no
        // records can be requested. Delete previously-emitted CRs.
        pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Gateway", gw.Name, gw.Namespace, nil)
        if perr != nil {
            logger.Error(perr, "orphan-prune failed during deactivation sweep")
        } else if len(pruned) > 0 {
            r.dedupe.emit(r.recorder, &gw, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
                fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
        }
        return reconcile.Result{}, nil
    }
```

- [ ] **Step 3.6: Run both tests to verify they pass**

```bash
go test ./internal/controller/tunnel/ -run 'TestGatewaySource_(OptOut|HostnamesCleared)_PrunesEmittedCRs' -v
```

Expected: both PASS.

- [ ] **Step 3.7: Run the full package test suite**

```bash
go test ./internal/controller/tunnel/...
```

Expected: PASS. In particular, `TestGatewaySource_OptOut_ClearsCache` and `TestGatewaySource_NoListenerHostname_RejectsWithEvent` should still pass — the new prune calls don't change cache behavior or event semantics.

- [ ] **Step 3.8: Commit**

```bash
git add internal/controller/tunnel/gateway_source_controller.go internal/controller/tunnel/gateway_source_controller_test.go
git commit -m "$(cat <<'EOF'
fix(gateway): prune emitted DNSRecord CRs on opt-out and on cleared listener hostnames

Two deactivation branches in the Gateway reconcile early-returned
without pruning previously-emitted CRs: (1) cloudflare.io/tunnel
annotation removed (the primary repro from issue #145, "remove all
cloudflare.io/* annotations"), and (2) all listener hostnames cleared.
Add prune calls at both branches.

Refs issue #145.
EOF
)"
```

---

## Task 4: Service — Prune at `!enabled`

**Files:**
- Modify: `internal/controller/tunnel/service_source_controller.go:104-123`
- Test: `internal/controller/tunnel/service_source_controller_test.go` (append new test)

- [ ] **Step 4.1: Write the failing test**

Append to `internal/controller/tunnel/service_source_controller_test.go`:

```go
// TestServiceSource_OptOut_PrunesEmittedCRs verifies issue #145 fix for the
// Service controller: a Service with cloudflare.io/tunnel="true" that emits
// DNSRecord CRs must have those CRs deleted when the annotation is removed.
// Note: TestServiceSource_OptOut_ClearsCache covers the cache-side of opt-out;
// this test covers the CR-side gap that was missed there.
func TestServiceSource_OptOut_PrunesEmittedCRs(t *testing.T) {
    svc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name: "svc", Namespace: "app-foo",
            Annotations: map[string]string{
                conventions.AnnotationTunnel:     "true",
                conventions.AnnotationTunnelName: "payments",
                conventions.AnnotationHostnames:  "foo.example.com",
            },
        },
        Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
    }
    tn := preCreatedTunnel("app-foo-payments", "app-foo")
    base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
        WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
    c := reconcilelib.SSATranslatingClient(t, base)

    cache := tunnelsynth.NewCache()
    r := &ServiceSourceReconciler{
        Client: c, Scheme: srcScheme(t), Cache: cache,
        DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
    }

    // Pass 1: opted in → 1 CR.
    _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
    require.NoError(t, err)
    var dnsList v2alpha1.CloudflareDNSRecordList
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")
    require.Equal(t, "Service", dnsList.Items[0].Labels[conventions.LabelSourceKind])

    // Mutate: remove cloudflare.io/tunnel → !enabled branch.
    var got corev1.Service
    require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "svc"}, &got))
    delete(got.Annotations, conventions.AnnotationTunnel)
    require.NoError(t, c.Update(context.Background(), &got))

    // Pass 2: !enabled → expect CR deleted.
    _, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
    require.NoError(t, err)
    require.NoError(t, c.List(context.Background(), &dnsList))
    require.Empty(t, dnsList.Items, "opt-out prune should delete the previously-emitted CR")
}
```

- [ ] **Step 4.2: Run the test to verify it fails**

```bash
go test ./internal/controller/tunnel/ -run TestServiceSource_OptOut_PrunesEmittedCRs -v
```

Expected: FAIL on `require.Empty(...)`.

- [ ] **Step 4.3: Add the prune call**

Edit `internal/controller/tunnel/service_source_controller.go` at the `!enabled` branch (currently lines 104–123). Insert after the existing `r.Cache.Clear(...)` for the derived key, before `return reconcile.Result{}, nil`:

```go
        if k, derr := DeriveTunnelName(svc.Namespace, svc.Annotations[conventions.AnnotationTunnelName]); derr == nil {
            r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: svc.Namespace, Name: k}, srcKey)
        }
        // Deactivation prune (issue #145): source opted out of the tunnel.
        // Delete previously-emitted CRs so they don't squat on Cloudflare-
        // side records. Best-effort: log and continue.
        pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Service", svc.Name, svc.Namespace, nil)
        if perr != nil {
            logger.Error(perr, "orphan-prune failed during deactivation sweep")
        } else if len(pruned) > 0 {
            r.dedupe.emit(r.Recorder, &svc, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
                fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
        }
        return reconcile.Result{}, nil
    }
```

Note: Service's existing happy-path prune uses `r.dedupe.emit(r.Recorder, &svc, ...)` (capital-R `Recorder`); match that here.

- [ ] **Step 4.4: Run the test to verify it passes**

```bash
go test ./internal/controller/tunnel/ -run TestServiceSource_OptOut_PrunesEmittedCRs -v
```

Expected: PASS.

- [ ] **Step 4.5: Run the full package test suite**

```bash
go test ./internal/controller/tunnel/...
```

Expected: PASS. `TestServiceSource_OptOut_ClearsCache` and `TestServiceSource_OptOutSweepsBothPoolAndNamed` must still pass.

- [ ] **Step 4.6: Commit**

```bash
git add internal/controller/tunnel/service_source_controller.go internal/controller/tunnel/service_source_controller_test.go
git commit -m "$(cat <<'EOF'
fix(service): prune emitted DNSRecord CRs on opt-out

The Service reconcile's !enabled branch (cloudflare.io/tunnel removed)
early-returned without pruning previously-emitted CRs. The end-of-
reconcile pruneOrphanedDNSRecords call was unreachable on this path,
so removing the annotation left CRs as orphans. Add a prune at the
!enabled branch to match the parallel fixes in HTTPRoute, TLSRoute,
and Gateway.

Refs issue #145.
EOF
)"
```

---

## Task 5: Full-suite verification and final commit

- [ ] **Step 5.1: Run the entire test suite**

```bash
make test
```

Expected: PASS (unit + envtest, per `Makefile`).

- [ ] **Step 5.2: Run linters**

```bash
make lint
```

Expected: PASS. If `golangci-lint` warns on the new code (e.g., unused-import after the edits), fix and re-run.

- [ ] **Step 5.3: Sanity-check the diff scope**

```bash
git diff --stat main..HEAD
```

Expected: 8 files touched (4 controllers + 4 test files). No CRD changes, no chart changes, no workflow changes.

```bash
git diff main..HEAD -- internal/controller/tunnel/orphan_prune.go
```

Expected: **empty diff**. The pruner itself is unchanged — we only added new call sites.

- [ ] **Step 5.4: Manual end-to-end check (optional, if cluster access available)**

This is an integration smoke test on a live cluster. Skip if no cluster is available.

1. Apply a Gateway with `cloudflare.io/tunnel: "true"` + a hostname listener.
2. Confirm a `CloudflareDNSRecord` CR appears.
3. `kubectl edit gateway <name>` and remove the `cloudflare.io/tunnel` annotation.
4. Within one reconcile cycle, the CR should be deleted (its finalizer will run a Cloudflare-side cleanup first).

Expected: the CR is gone; `kubectl get cloudflarednsrecord -n <ns>` shows none for that source.

---

## Spec Coverage Audit

Mapping each design-doc requirement back to a task:

| Design section | Task(s) |
|---|---|
| HTTPRoute `parent == nil` prune | Task 1 |
| TLSRoute `parent == nil` prune | Task 2 |
| Gateway `!enabled` prune | Task 3 |
| Gateway `len(hostnames) == 0` prune | Task 3 |
| Service `!enabled` prune | Task 4 |
| Error handling: log-and-continue, distinct message | All tasks (call uses `"orphan-prune failed during deactivation sweep"`) |
| Test acceptance: fails without code, passes with code | Step X.2 (fails) → Step X.4 (passes) in each task |
| No changes to NotFound branches | Verified by Step 5.3 (only the 4 controller files modified, only at the documented branches) |
| No changes to transient branches (deferred emission, apex-blocked) | Verified by Step 5.3 (no edits outside the documented branches) |
| No changes to `pruneOrphanedDNSRecords` itself | Verified by Step 5.3's targeted diff |
| No new abstractions, no signature changes | All tasks call the existing helper with `desired=nil` |

## Notes for Reviewers

- **Why `nil` instead of `map[string]struct{}{}`:** Go's `nil` map is safe to read from (`_, ok := nilMap[k]` returns the zero value and `false`); the pruner uses exactly that pattern. Passing `nil` is one fewer allocation and reads more clearly as "no desired set" than an empty literal.
- **Why no new convention constant:** The event reason (`ReasonOrphanedDNSRecordPruned`) is reused intentionally. The semantic — "we pruned CRs this source previously emitted" — is identical between the happy-path-prune call and the deactivation-prune call; the message text disambiguates the two.
- **Why the prune appears after `Cache.Clear`:** consistency. The cache sweep is a pure in-memory operation; the prune touches the API server. Doing in-memory work first matches the existing pattern.
- **Why the `resolveGatewayService(...)` error branch is not modified:** the design's "Open questions" section documents this. The error mixes "annotation missing" (definitive) and "service Get failed" (transient); without splitting the error type, a prune at this branch would risk false-positive deletes on transient API-server issues. Disambiguating via `errGatewayServiceAnnotationMissing` is a viable follow-up if field reports show real orphans.
