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

// Package zone wires the four zone-bundle reconcilers into a controller-runtime manager.
package zone

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
)

// Options carry per-process configuration for the zone bundle.
//
// Per-reconcile credentials are resolved via reconcile.LoadCredentialsHierarchical
// (Foundation T12) — no static creds are held here.
//
// This struct is intentionally empty in this phase; it exists as a forward-
// compatible seat for future options (interval defaults, TXT-registry codec
// configuration when the companion registry lands, etc.).
type Options struct{}

// AddToManager registers all four zone-bundle reconcilers with mgr.
// Caller is responsible for leader-election and signal-handling — the
// controller-runtime manager wires those.
func AddToManager(mgr ctrl.Manager, _ Options) error {
	scheme := mgr.GetScheme()
	c := mgr.GetClient()
	rec := mgr.GetEventRecorderFor("cloudflare-operator-zone")

	cfClientFn := cloudflare.NewClient
	zoneFn := func(creds cloudflare.Credentials) (cloudflare.ZoneClient, error) {
		cf, err := cfClientFn(creds)
		if err != nil {
			return nil, err
		}
		return cloudflare.NewZoneClientFromCF(cf.CF()), nil
	}
	dnsFn := func(creds cloudflare.Credentials) (cloudflare.DNSClient, error) {
		cf, err := cfClientFn(creds)
		if err != nil {
			return nil, err
		}
		return cloudflare.NewDNSClientFromCF(cf.CF()), nil
	}
	rsFn := func(creds cloudflare.Credentials) (cloudflare.RulesetClient, error) {
		cf, err := cfClientFn(creds)
		if err != nil {
			return nil, err
		}
		return cloudflare.NewRulesetClientFromCF(cf.CF()), nil
	}
	zcFn := func(creds cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) {
		cf, err := cfClientFn(creds)
		if err != nil {
			return nil, err
		}
		return cloudflare.NewZoneConfigClientFromCF(cf.CF()), nil
	}

	zoneR := &CloudflareZoneReconciler{
		Client: c, Scheme: scheme, Recorder: rec,
		ZoneClientFn: zoneFn, CFClientFn: cfClientFn,
	}
	if err := ctrl.NewControllerManagedBy(mgr).For(&v2alpha1.CloudflareZone{}).Complete(zoneR); err != nil {
		return fmt.Errorf("setup CloudflareZone: %w", err)
	}

	zcR := &CloudflareZoneConfigReconciler{
		Client: c, Scheme: scheme, Recorder: rec,
		ZoneConfigClientFn: zcFn,
	}
	if err := ctrl.NewControllerManagedBy(mgr).For(&v2alpha1.CloudflareZoneConfig{}).Complete(zcR); err != nil {
		return fmt.Errorf("setup CloudflareZoneConfig: %w", err)
	}

	dnsR := &CloudflareDNSRecordReconciler{
		Client: c, Scheme: scheme, Recorder: rec,
		DNSClientFn: dnsFn,
		IPResolver:  ipresolver.NewResolver(),
	}
	if err := ctrl.NewControllerManagedBy(mgr).For(&v2alpha1.CloudflareDNSRecord{}).Complete(dnsR); err != nil {
		return fmt.Errorf("setup CloudflareDNSRecord: %w", err)
	}

	rsR := &CloudflareRulesetReconciler{
		Client: c, Scheme: scheme, Recorder: rec,
		RulesetClientFn: rsFn,
	}
	if err := ctrl.NewControllerManagedBy(mgr).For(&v2alpha1.CloudflareRuleset{}).Complete(rsR); err != nil {
		return fmt.Errorf("setup CloudflareRuleset: %w", err)
	}
	return nil
}
