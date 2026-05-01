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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrNoServicePorts is returned by resolveServiceBackend when the Service
// has no ports defined.
var ErrNoServicePorts = errors.New("service has no ports")

// ErrPortNotFound is returned by resolveServiceBackend when the requested port
// name or number is not present on the Service.
var ErrPortNotFound = errors.New("port not found on Service")

// ServiceSourceReconciler watches Services that carry cloudflare.io/*
// annotations and emits CloudflareDNSRecord + CloudflareTunnelRule CRs.
type ServiceSourceReconciler struct {
	client.Client
	Recorder    record.EventRecorder
	TxtOwnerID  string
	AffixConfig cfclient.AffixConfig
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnelrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile processes a single Service reconcile request.
func (r *ServiceSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the Service.
	var svc corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Service: %w", err)
	}

	ann := svc.Annotations
	if ann == nil {
		return ctrl.Result{}, nil
	}

	// 2. Check for cloudflare.io/target annotation.
	rawTarget, ok := ann[AnnotationTarget]
	if !ok || rawTarget == "" {
		return ctrl.Result{}, nil
	}

	// 3. Require TxtOwnerID.
	if r.TxtOwnerID == "" {
		r.Recorder.Event(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"TXT_OWNER_ID is not configured; skipping Service source")
		return ctrl.Result{}, nil
	}

	// 4. Parse target annotation.
	ts, err := ParseTarget(rawTarget)
	if err != nil {
		r.Recorder.Eventf(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"invalid cloudflare.io/target %q: %v", rawTarget, err)
		return ctrl.Result{}, nil
	}

	// 5. address target is not supported on Services.
	if ts.Kind == TargetKindAddress {
		r.Recorder.Eventf(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"target kind %q is not supported on Services", ts.Kind)
		return ctrl.Result{}, nil
	}

	// 6. Parse hostnames.
	hostnamesRaw := ann[AnnotationHostnames]
	if hostnamesRaw == "" {
		r.Recorder.Event(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"cloudflare.io/hostnames is required")
		return ctrl.Result{}, nil
	}
	hostnames := splitAndTrim(hostnamesRaw)
	if len(hostnames) == 0 {
		r.Recorder.Event(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"cloudflare.io/hostnames is empty")
		return ctrl.Result{}, nil
	}

	// 7. Validate all hostnames.
	for _, h := range hostnames {
		if !isValidDNSName(h) {
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
				"hostname %q is not a valid DNS name", h)
			return ctrl.Result{}, nil
		}
	}

	// 8. Resolve backend (Service-specific).
	backend, err := r.resolveServiceBackend(&svc)
	if err != nil {
		reason := cloudflarev1alpha1.ReasonInvalidAnnotation
		r.Recorder.Eventf(&svc, corev1.EventTypeWarning, reason,
			"cannot resolve backend: %v", err)
		return ctrl.Result{}, nil
	}

	// 9. For tunnel targets, resolve the tunnel CNAME.
	var tunnelCNAME string
	var tunnelNs string
	if ts.Kind == TargetKindTunnel {
		cname, ready, err := resolveTunnelCNAME(ctx, r.Client, svc.Namespace, ann, ts.Name)
		if err != nil {
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonTunnelNotFound,
				"cannot resolve tunnel %q: %v", ts.Name, err)
			return ctrl.Result{}, nil
		}
		if !ready {
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonTunnelNotReady,
				"tunnel %q has no CNAME yet; requeuing", ts.Name)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		tunnelCNAME = cname
		tunnelNs = firstNonEmpty(ann[AnnotationTunnelRefNamespace], svc.Namespace)
	}

	// 10. Determine proxied flag. Default is true for all target kinds; tunnel
	// targets always force true regardless of the annotation.
	proxied := true
	if ts.Kind != TargetKindTunnel {
		// Non-tunnel: allow the annotation to override.
		if raw, ok := ann[AnnotationProxied]; ok && raw != "" {
			if v, err := strconv.ParseBool(raw); err == nil {
				proxied = v
			}
		}
	}
	// Tunnels MUST be proxied — cannot be turned off via annotation.
	if ts.Kind == TargetKindTunnel {
		proxied = true
	}

	// 11. TTL.
	ttl := ttlFromAnnotation(ann)

	// 12. Build owner references.
	svc.TypeMeta = metav1.TypeMeta{
		APIVersion: "v1",
		Kind:       "Service",
	}
	ownerRefs := ownerRefsFor(&svc)

	// 13. Source labels.
	sourceLabels := map[string]string{
		LabelSourceKind:      "Service",
		LabelSourceNamespace: svc.Namespace,
		LabelSourceName:      svc.Name,
		LabelManagedBy:       "cloudflare-operator",
	}

	// 14. Emit DNS records for each hostname + companion TXT.
	for _, h := range hostnames {
		zone, err := resolveZoneRefFromAnnotations(ctx, r.Client, svc.Namespace, ann, h)
		if err != nil {
			reason := cloudflarev1alpha1.ReasonNoMatchingZone
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, reason,
				"zone resolution failed for hostname %q: %v", h, err)
			return ctrl.Result{}, nil
		}
		content := contentForTarget(ts, tunnelCNAME)
		if err := r.emitDNSPair(ctx, &svc, h, content, zone, proxied, ttl, sourceLabels, ownerRefs, ann); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 15. Emit TunnelRule when target is tunnel.
	if ts.Kind == TargetKindTunnel {
		ruleName := fmt.Sprintf("svc-%s-%s", svc.Namespace, svc.Name)
		rule := &cloudflarev1alpha1.CloudflareTunnelRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ruleName,
				Namespace:       svc.Namespace,
				Labels:          sourceLabels,
				OwnerReferences: ownerRefs,
			},
			Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
				TunnelRef: cloudflarev1alpha1.TunnelReference{
					Name:      ts.Name,
					Namespace: tunnelNs,
				},
				Hostnames: hostnames,
				Backend: cloudflarev1alpha1.TunnelRuleBackend{
					ServiceRef: backend,
				},
				Priority: 100,
				SourceRef: &cloudflarev1alpha1.TunnelRuleSourceRef{
					APIVersion: "v1",
					Kind:       "Service",
					Namespace:  svc.Namespace,
					Name:       svc.Name,
					UID:        string(svc.UID),
				},
			},
		}
		if err := upsertTunnelRule(ctx, r.Client, rule); err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert TunnelRule: %w", err)
		}
	}

	logger.V(1).Info("reconciled Service source",
		"service", req.NamespacedName,
		"hostnames", hostnames,
		"target", rawTarget)

	r.Recorder.Eventf(&svc, corev1.EventTypeNormal, cloudflarev1alpha1.ReasonDNSReconciled,
		"DNS records and tunnel rules reconciled for %d hostname(s)", len(hostnames))

	return ctrl.Result{}, nil
}

// contentForTarget returns the DNS record content string for the given target.
func contentForTarget(ts TargetSpec, tunnelCNAME string) string {
	switch ts.Kind {
	case TargetKindTunnel:
		return tunnelCNAME
	case TargetKindCNAME:
		return ts.CNAME
	default:
		return ""
	}
}

// emitDNSPair creates or updates the CNAME record and its companion TXT
// registry record for a single hostname.
func (r *ServiceSourceReconciler) emitDNSPair(
	ctx context.Context,
	svc *corev1.Service,
	hostname string,
	content string,
	zone *cloudflarev1alpha1.CloudflareZone,
	proxied bool,
	ttl int,
	labels map[string]string,
	ownerRefs []metav1.OwnerReference,
	sourceAnnotations map[string]string,
) error {
	const recordType = "CNAME"
	crName := capCRName(fmt.Sprintf("svc-%s-%s-%s", svc.Namespace, svc.Name, sanitizeDNSForCRName(hostname)))
	// Propagate cloudflare.io/adopt from the source object to the emitted CR so
	// the DNS controller's registry decision can honour it.
	var dnsRecordAnnotations map[string]string
	if sourceAnnotations[AnnotationAdopt] == AnnotationValueTrue {
		dnsRecordAnnotations = map[string]string{AnnotationAdopt: AnnotationValueTrue}
	}
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crName,
			Namespace:       svc.Namespace,
			Labels:          labels,
			Annotations:     dnsRecordAnnotations,
			OwnerReferences: ownerRefs,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      hostname,
			Type:      recordType,
			Content:   strPtr(content),
			Proxied:   boolPtr(proxied),
			TTL:       ttl,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: zone.Spec.SecretRef.Name},
			ZoneRef: &cloudflarev1alpha1.ZoneReference{
				Name:      zone.Name,
				Namespace: zone.Namespace,
			},
		},
	}
	if err := upsertDNSRecord(ctx, r.Client, dnsRecord); err != nil {
		return fmt.Errorf("upsert DNS record for %q: %w", hostname, err)
	}

	txtFQDN := cfclient.AffixName(hostname, recordType, r.AffixConfig)
	txtContent := cfclient.EncodeRegistryPayload(cfclient.RegistryPayload{
		Owner:           r.TxtOwnerID,
		SourceKind:      "Service",
		SourceNamespace: svc.Namespace,
		SourceName:      svc.Name,
	})
	txtCRName := capCRName(fmt.Sprintf("svc-%s-%s-%s-txt", svc.Namespace, svc.Name, sanitizeDNSForCRName(hostname)))
	txtRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      txtCRName,
			Namespace: svc.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				AnnotationRegistryFor: hostname,
			},
			OwnerReferences: ownerRefs,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      txtFQDN,
			Type:      "TXT",
			Content:   strPtr(txtContent),
			TTL:       120,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: zone.Spec.SecretRef.Name},
			ZoneRef: &cloudflarev1alpha1.ZoneReference{
				Name:      zone.Name,
				Namespace: zone.Namespace,
			},
		},
	}
	if err := upsertDNSRecord(ctx, r.Client, txtRecord); err != nil {
		return fmt.Errorf("upsert TXT record for %q: %w", hostname, err)
	}
	return nil
}

// resolveServiceBackend determines the TunnelRuleServiceRef for a Service.
// It picks the first port (or the port matching cloudflare.io/port annotation)
// and infers the scheme from the port name.
//
// Named and numeric port annotations are disambiguated numeric-first: an
// annotation value that parses as an integer matches a port by Port number
// before falling through to a name match.
func (r *ServiceSourceReconciler) resolveServiceBackend(svc *corev1.Service) (*cloudflarev1alpha1.TunnelRuleServiceRef, error) {
	if len(svc.Spec.Ports) == 0 {
		return nil, ErrNoServicePorts
	}

	ann := svc.Annotations
	if ann == nil {
		ann = map[string]string{}
	}

	var port corev1.ServicePort
	portAnnotation := ann[AnnotationPort]

	if portAnnotation == "" {
		// Use first port.
		port = svc.Spec.Ports[0]
	} else {
		// Try matching by name first, then by number.
		found := false
		// Try numeric.
		if num, err := strconv.Atoi(portAnnotation); err == nil {
			for _, p := range svc.Spec.Ports {
				if int(p.Port) == num {
					port = p
					found = true
					break
				}
			}
		}
		if !found {
			// Try by name.
			for _, p := range svc.Spec.Ports {
				if p.Name == portAnnotation {
					port = p
					found = true
					break
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrPortNotFound, portAnnotation)
		}
	}

	schemeOverride := ann[AnnotationScheme]
	scheme := inferScheme(schemeOverride, port.Name)

	return &cloudflarev1alpha1.TunnelRuleServiceRef{
		Name:      svc.Name,
		Namespace: svc.Namespace,
		Port:      intstr.FromInt(int(port.Port)),
		Scheme:    scheme,
	}, nil
}

// inferScheme returns the cloudflared scheme (http, https, h2c, tcp) to use
// for a backend. The override annotation value wins; otherwise the port name
// is inspected for well-known patterns.
func inferScheme(override, portName string) string {
	if override != "" {
		return override
	}
	lower := strings.ToLower(portName)
	switch {
	case strings.Contains(lower, "https") || strings.Contains(lower, "tls") || strings.Contains(lower, "ssl"):
		return "https"
	case strings.Contains(lower, "h2c") || strings.Contains(lower, "grpc"):
		return "h2c"
	default:
		return "http"
	}
}

// mapTunnelToServices returns reconcile requests for all Services that reference
// the updated CloudflareTunnel. Services are listed cluster-wide so that
// cross-namespace references (Service in namespace A referencing a tunnel in
// namespace B via cloudflare.io/tunnel-ref-namespace) are enqueued promptly
// instead of waiting for periodic resync.
//
// Namespace resolution: if a Service carries cloudflare.io/tunnel-ref-namespace,
// that value is used as the tunnel namespace; otherwise the Service's own
// namespace is assumed. Only Services whose resolved tunnel namespace matches
// the updated tunnel's namespace are enqueued.
func (r *ServiceSourceReconciler) mapTunnelToServices(ctx context.Context, obj client.Object) []reconcile.Request {
	tun, ok := obj.(*cloudflarev1alpha1.CloudflareTunnel)
	if !ok {
		return nil
	}
	var list corev1.ServiceList
	if err := r.List(ctx, &list); err != nil { // cluster-wide, no InNamespace
		return nil
	}
	out := make([]reconcile.Request, 0)
	for i := range list.Items {
		svc := &list.Items[i]
		ann := svc.Annotations
		if ann == nil {
			continue
		}
		if ann[AnnotationTarget] != "tunnel:"+tun.Name {
			continue
		}
		refNs := ann[AnnotationTunnelRefNamespace]
		if refNs == "" {
			refNs = svc.Namespace
		}
		if refNs != tun.Namespace {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name},
		})
	}
	return out
}

// SetupWithManager registers the ServiceSourceReconciler with the manager.
func (r *ServiceSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Watches(
			&cloudflarev1alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(r.mapTunnelToServices),
		).
		Complete(r)
}
