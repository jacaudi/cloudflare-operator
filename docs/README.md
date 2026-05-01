# Documentation

This directory holds the topical documentation for cloudflare-operator. Each file is the canonical reference for its subject — start here, follow the link to the doc that matches what you're trying to do.

## What is this project?

cloudflare-operator is a Kubernetes operator that manages Cloudflare resources (zones, DNS records, tunnels, zone settings, rulesets) declaratively via Custom Resources in the `cloudflare.io/v1alpha1` API group.

It is a **community project**, **not an official Cloudflare product**, and is not endorsed by or affiliated with Cloudflare, Inc. All Cloudflare API access is implemented on top of the official [`cloudflare/cloudflare-go`](https://github.com/cloudflare/cloudflare-go) Go SDK.

For a one-page overview, the operator's CRDs and Helm install instructions, see the [project root README](../README.md).

---

## Topical guides

| Doc | Read this when… |
|---|---|
| [**`domain-onboarding.md`**](domain-onboarding.md) | First-time setup. End-to-end walkthrough: API token → credentials Secret → first zone → first DNS record → first tunnel. |
| [**`gateway-api-source.md`**](gateway-api-source.md) | You want to drive DNS and tunnel ingress from `HTTPRoute`, parent `Gateway`, or `Service` annotations (the v1 primary user interface). Annotation reference, inheritance rules, multi-zone resolution, worked examples. |
| [**`tunnels.md`**](tunnels.md) | You're using `CloudflareTunnel` with `connector.enabled: true` (operator-managed `cloudflared`), or you author `CloudflareTunnelRule` CRs. Connector spec deep-dive, ingress aggregation semantics, sort/conflict rules, migration from self-managed cloudflared. |
| [**`external-dns-migration.md`**](external-dns-migration.md) | You're moving from external-dns. Three migration paths (drop-in same-owner, parallel different-owner, greenfield), TXT registry handover, batch adoption. |
| [**`troubleshooting.md`**](troubleshooting.md) | Something isn't working. Symptom-indexed. DNS not appearing, wrong record content, tunnel not serving a hostname, connector pod not Ready, ownership conflicts, hostname collisions. |
| [**`crd-reference.md`**](crd-reference.md) | You need a field-by-field spec for any CRD: `CloudflareZone`, `CloudflareDNSRecord`, `CloudflareTunnel`, `CloudflareTunnelRule`, `CloudflareZoneConfig`, `CloudflareRuleset`. Defaults, behavior, status conditions, examples, print columns. |

---

## Cross-references

- **Helm chart values** — [`chart/values.yaml`](../chart/values.yaml) is the authoritative list of every chart configuration option.
- **Sample manifests** — [`config/samples/`](../config/samples) contains runnable YAML for each CRD.
- **Release notes** — [`CHANGELOG.md`](../CHANGELOG.md) and [GitHub Releases](https://github.com/jacaudi/cloudflare-operator/releases).
- **Project conventions** — [`AGENTS.md`](../AGENTS.md) describes the kubebuilder layout, generated-file boundaries, and controller patterns the codebase follows.
