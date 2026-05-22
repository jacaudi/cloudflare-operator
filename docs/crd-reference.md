# API Reference

## Packages
- [cloudflare.io/v2alpha1](#cloudflareiov2alpha1)


## cloudflare.io/v2alpha1

Package v2alpha1 contains API Schema definitions for the cloudflare.io v2alpha1 API group.

### Resource Types
- [CloudflareDNSRecord](#cloudflarednsrecord)
- [CloudflareRuleset](#cloudflareruleset)
- [CloudflareTunnel](#cloudflaretunnel)
- [CloudflareZone](#cloudflarezone)
- [CloudflareZoneConfig](#cloudflarezoneconfig)





---

## CloudflareDNSRecord



CloudflareDNSRecord is the Schema for the cloudflarednsrecords API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cloudflare.io/v2alpha1` | | |
| `kind` _string_ | `CloudflareDNSRecord` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CloudflareDNSRecordSpec](#cloudflarednsrecordspec)_ |  |  |  |
| `status` _[CloudflareDNSRecordStatus](#cloudflarednsrecordstatus)_ |  |  |  |



---

## CloudflareRuleset



CloudflareRuleset is the Schema for the cloudflarerulesets API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cloudflare.io/v2alpha1` | | |
| `kind` _string_ | `CloudflareRuleset` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CloudflareRulesetSpec](#cloudflarerulesetspec)_ |  |  |  |
| `status` _[CloudflareRulesetStatus](#cloudflarerulesetstatus)_ |  |  |  |



---

## CloudflareTunnel



CloudflareTunnel is the Schema for the cloudflaretunnels API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cloudflare.io/v2alpha1` | | |
| `kind` _string_ | `CloudflareTunnel` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CloudflareTunnelSpec](#cloudflaretunnelspec)_ |  |  |  |
| `status` _[CloudflareTunnelStatus](#cloudflaretunnelstatus)_ |  |  |  |



---

## CloudflareZone



CloudflareZone is the Schema for the cloudflarezones API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cloudflare.io/v2alpha1` | | |
| `kind` _string_ | `CloudflareZone` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CloudflareZoneSpec](#cloudflarezonespec)_ |  |  |  |
| `status` _[CloudflareZoneStatus](#cloudflarezonestatus)_ |  |  |  |

---

## CloudflareZoneConfig



CloudflareZoneConfig is the Schema for the cloudflarezoneconfigs API





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `cloudflare.io/v2alpha1` | | |
| `kind` _string_ | `CloudflareZoneConfig` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)_ | spec defines the desired state of CloudflareZoneConfig |  | Optional: \{\} <br /> |
| `status` _[CloudflareZoneConfigStatus](#cloudflarezoneconfigstatus)_ | status defines the observed state of CloudflareZoneConfig |  | Optional: \{\} <br /> |































---

### Sub-types

The types below are referenced by one or more of the CRDs above; they are
never instantiated directly.

#### AttachedSource



AttachedSource identifies one source object contributing to this tunnel.
Fields are immutable post-create from the source reconciler's perspective.



_Appears in:_
- [CloudflareTunnelStatus](#cloudflaretunnelstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kind` _string_ | Kind is one of Service / Gateway / HTTPRoute / TLSRoute. |  | Required: \{\} <br /> |
| `name` _string_ | Name of the source object. |  | Required: \{\} <br /> |
| `namespace` _string_ | Namespace of the source object. |  | Required: \{\} <br /> |

#### BotManagementSettings



BotManagementSettings defines bot management settings for a Cloudflare zone.

Configuring this section requires the Zone:Bot Management:Edit scope on the
API token and a Cloudflare plan that supports bot management. On Free plans
this section's API call returns 403; the controller will surface that on
the BotManagementApplied condition with reason=PlanTierInsufficient without
preventing other groups (ssl / security / performance / network) from
being applied.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enableJS` _boolean_ | EnableJS enables JavaScript detections. |  | Optional: \{\} <br /> |
| `fightMode` _boolean_ | FightMode enables bot fight mode. |  | Optional: \{\} <br /> |

#### CloudflareCredentialRef



CloudflareCredentialRef bundles the credential Secret and account ID.
Per Foundation §5 these are inherited or overridden as a unit.



_Appears in:_
- [CloudflareDNSRecordSpec](#cloudflarednsrecordspec)
- [CloudflareRulesetSpec](#cloudflarerulesetspec)
- [CloudflareTunnelSpec](#cloudflaretunnelspec)
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)
- [CloudflareZoneSpec](#cloudflarezonespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `tokenSecretRef` _[SecretReference](#secretreference)_ | TokenSecretRef points at the Secret carrying the Cloudflare API token. |  |  |
| `accountID` _string_ | AccountID is the Cloudflare account ID this credential scopes to.<br />Exactly one of accountID or accountIDSecretRef must be set. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `accountIDSecretRef` _[SecretReference](#secretreference)_ | AccountIDSecretRef resolves the Cloudflare account ID from a Secret<br />instead of the inline accountID (exactly one of the two must be set).<br />NOTE: SecretReference.Key defaults to "token"; set key: accountID<br />explicitly (the account ID is typically a distinct key in the same<br />Secret as the API token). |  | Optional: \{\} <br /> |
| `txtRegistryKeySecretRef` _[SecretReference](#secretreference)_ | TxtRegistryKeySecretRef references a Secret holding an AES-256 key<br />(exactly 32 bytes, under the SecretReference.Key entry, default "key").<br />When set, the DNSRecord reconciler encrypts TXT companion-registry<br />payloads with AES-256-GCM (wire format v1:<base64-nonce>:<base64-ct>);<br />when unset, companions are written as plaintext JSON. The read side<br />auto-detects either form. See the TXT-registry design for the full<br />contract (companion naming, ownership verification, observe mode). |  | Optional: \{\} <br /> |


#### CloudflareDNSRecordSpec



CloudflareDNSRecordSpec defines the desired state of a Cloudflare DNS record.



_Appears in:_
- [CloudflareDNSRecord](#cloudflarednsrecord)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `zoneID` _string_ | ZoneID is the Cloudflare Zone ID. Mutually exclusive with ZoneRef. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `zoneRef` _[ZoneReference](#zonereference)_ | ZoneRef references a CloudflareZone CR. Mutually exclusive with ZoneID. |  | Optional: \{\} <br /> |
| `name` _string_ | Name is the DNS record name (e.g., "example.com", "sub.example.com"). |  | MinLength: 1 <br />Required: \{\} <br /> |
| `type` _string_ | Type is the DNS record type. |  | Enum: [A AAAA CNAME SRV MX TXT NS] <br />Required: \{\} <br /> |
| `content` _string_ | Content is the record content (IP, hostname, etc.). XOR with DynamicIP. |  | Optional: \{\} <br /> |
| `dynamicIP` _boolean_ | DynamicIP enables automatic external IP resolution. Only valid for A/AAAA.<br />XOR with Content. |  | Optional: \{\} <br /> |
| `ttl` _integer_ | TTL in seconds. Use 1 for automatic. | 1 | Minimum: 1 <br />Optional: \{\} <br /> |
| `proxied` _boolean_ | Proxied indicates whether the record is proxied through Cloudflare. |  | Optional: \{\} <br /> |
| `srvData` _[SRVData](#srvdata)_ | SRVData contains SRV-specific record fields. Required when Type=SRV. |  | Optional: \{\} <br /> |
| `priority` _integer_ | Priority is the MX record priority (lower = preferred). SRV records use<br />srvData.priority instead. |  | Optional: \{\} <br /> |
| `adopt` _boolean_ | Adopt, when true, lets the operator take over a pre-existing Cloudflare<br />record instead of creating a new one. Adoption is TXT-ownership-verified:<br />the operator only adopts a record whose companion TXT registry entry<br />identifies THIS CloudflareDNSRecord. A record with no companion, a<br />foreign companion, or an unparseable one is refused<br />(AdoptRefusedNoTXT / AdoptRefusedForeign) — there is no silent backfill.<br />Pre-feature adopted records must be migrated via the documented<br />TXT-registry migration procedure (design §5.4) before Adopt succeeds. |  | Optional: \{\} <br /> |
| `mode` _[RecordMode](#recordmode)_ | Mode controls operator write behavior on this record.<br />Default Managed: operator creates / updates / deletes the underlying<br />Cloudflare record and TXT companion as needed.<br />Observe: operator reads but never writes. Useful for verifying state<br />before claiming a record under Adopt:true (which would otherwise<br />refuse without a matching TXT companion). | Managed | Enum: [Managed Observe] <br />Optional: \{\} <br /> |
| `cloudflare` _[CloudflareCredentialRef](#cloudflarecredentialref)_ | Cloudflare overrides the operator-level default credential (sourced<br />from the operator's CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID env,<br />chart-set from a Secret). Per Foundation §5 the token and accountID<br />are inherited or overridden as a unit; CEL rejects mixing.<br />Omitted entirely → the operator-level env default applies. |  | Optional: \{\} <br /> |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#duration-v1-meta)_ | Interval is the reconciliation interval for drift detection. | 5m | Optional: \{\} <br /> |

#### CloudflareDNSRecordStatus



CloudflareDNSRecordStatus defines the observed state.



_Appears in:_
- [CloudflareDNSRecord](#cloudflarednsrecord)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#condition-v1-meta) array_ |  |  | Optional: \{\} <br /> |
| `phase` _[Phase](#phase)_ | Phase is a coarse summary derived from the Ready condition (Foundation §8). | Pending | Enum: [Ready Reconciling Error Pending] <br />Optional: \{\} <br /> |
| `recordID` _string_ | RecordID is the Cloudflare ID of the managed DNS record. |  | Optional: \{\} <br /> |
| `currentContent` _string_ | CurrentContent is the most-recently-observed record content (post-resolve<br />for DynamicIP). |  | Optional: \{\} <br /> |
| `lastSyncedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ | LastSyncedAt is the timestamp of the most recent successful reconcile. |  | Optional: \{\} <br /> |
| `txtRecordID` _string_ | TxtRecordID is the Cloudflare-side ID of the companion TXT record.<br />Empty when no TXT companion has been written yet. Set on successful<br />TXT write; cleared on delete. |  | Optional: \{\} <br /> |
| `txtAffix` _string_ | TxtAffix is the prefix used for the companion TXT record name (today<br />always "cf-txt"). Recorded for forensic clarity if the convention<br />changes (e.g., v2 affixing scheme). Operator-managed; users should<br />not edit. |  | Optional: \{\} <br /> |
| `observedTXT` _[ObservedTXTPayload](#observedtxtpayload)_ | ObservedTXT carries the decoded TXT companion payload as last<br />observed from Cloudflare. Populated by both Managed and Observe modes<br />when a TXT companion exists. RawContent is set instead when decoding<br />fails. |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the .metadata.generation observed by the controller<br />during its last reconcile. When this lags .metadata.generation the<br />controller has not yet processed the latest spec. |  | Optional: \{\} <br /> |
| `lastReconcileToken` _string_ | LastReconcileToken is the controller-owned ack of the most recent<br />cloudflare.io/reconcile-at annotation value the controller has<br />observed. The prelude in internal/reconcile.ForceReconcileRequested<br />compares this against the live annotation; mismatch forces a full<br />re-check this reconcile (bypassing the change-detection short-<br />circuit). The operator NEVER modifies the annotation itself — only<br />this status field — so admin force-triggers are not auto-cleared. |  | Optional: \{\} <br /> |
| `legacyCompanionGCDone` _boolean_ | LegacyCompanionGCDone marks a record as having completed the one-time<br />legacy-name companion GC sweep. When true, gcLegacyCompanion is<br />skipped on subsequent reconciles. Stamped after a successful pass<br />that either (a) found no legacy candidates, or (b) successfully<br />deleted a legacy companion. Pre-S1 CRs reconcile once, set the<br />field, and never pay the GC cost again. Purely additive: existing<br />CRs without the field behave like field=false on first reconcile. |  | Optional: \{\} <br /> |


#### CloudflareRulesetSpec



CloudflareRulesetSpec defines the desired state of CloudflareRuleset.



_Appears in:_
- [CloudflareRuleset](#cloudflareruleset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `zoneID` _string_ | ZoneID is the Cloudflare Zone ID. Mutually exclusive with ZoneRef. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `zoneRef` _[ZoneReference](#zonereference)_ | ZoneRef references a CloudflareZone CR. Mutually exclusive with ZoneID. |  | Optional: \{\} <br /> |
| `cloudflare` _[CloudflareCredentialRef](#cloudflarecredentialref)_ | Cloudflare overrides the top-level credential + account. |  | Optional: \{\} <br /> |
| `name` _string_ | Name is the human-readable name for the ruleset. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `description` _string_ | Description is an informative description of the ruleset. |  | Optional: \{\} <br /> |
| `phase` _string_ | Phase is the Cloudflare ruleset entrypoint phase. This is the<br />Cloudflare API surface (not the operator's lifecycle Phase). |  | Enum: [http_request_firewall_custom http_request_firewall_managed http_request_late_transform http_request_redirect http_request_transform http_response_headers_transform http_response_firewall_managed http_config_settings http_custom_errors http_ratelimit http_request_cache_settings http_request_origin http_request_dynamic_redirect http_response_compression] <br />Required: \{\} <br /> |
| `rules` _[RulesetRuleSpec](#rulesetrulespec) array_ | Rules is the list of rules in the ruleset. |  | MinItems: 1 <br />Required: \{\} <br /> |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#duration-v1-meta)_ | Interval is the reconciliation interval. | 30m | Optional: \{\} <br /> |

#### CloudflareRulesetStatus



CloudflareRulesetStatus defines the observed state of CloudflareRuleset.



_Appears in:_
- [CloudflareRuleset](#cloudflareruleset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |
| `rulesetID` _string_ | RulesetID is the Cloudflare Ruleset ID. |  | Optional: \{\} <br /> |
| `ruleCount` _integer_ | RuleCount is the number of rules in the ruleset. |  | Optional: \{\} <br /> |
| `lastSyncedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ | LastSyncedAt is the last time the ruleset was successfully synced. |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recently observed generation of the CR. |  | Optional: \{\} <br /> |
| `phase` _[Phase](#phase)_ | Phase is a coarse summary of the reconciliation state. See<br />Phase for the enum values. | Pending | Enum: [Ready Reconciling Error Pending] <br />Optional: \{\} <br /> |
| `lastReconcileToken` _string_ | LastReconcileToken is the controller-owned ack of the most recent<br />cloudflare.io/reconcile-at annotation value the controller has<br />observed. The prelude in internal/reconcile.ForceReconcileRequested<br />compares this against the live annotation; mismatch forces a full<br />re-check this reconcile (bypassing the change-detection short-<br />circuit). The operator NEVER modifies the annotation itself — only<br />this status field — so admin force-triggers are not auto-cleared. |  | Optional: \{\} <br /> |


#### CloudflareTunnelSpec



CloudflareTunnelSpec defines the desired state of a Cloudflare Tunnel.



_Appears in:_
- [CloudflareTunnel](#cloudflaretunnel)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the tunnel name in Cloudflare. Immutable after create — the<br />Cloudflare API treats config_src as write-once; renames would orphan<br />the cloudflared credential Secret and DNS targets. Capped at 52<br />characters so derived resource names (cloudflared-<tunnel-name>) fit<br />the 63-character DNS-1123 label limit. |  | MaxLength: 52 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `connector` _[ConnectorSpec](#connectorspec)_ | Connector configures the operator-managed cloudflared Deployment. |  | Required: \{\} <br /> |
| `cloudflare` _[CloudflareCredentialRef](#cloudflarecredentialref)_ | Cloudflare overrides the operator-level credential + accountID.<br />Per Foundation §5: credential and accountID inherited or overridden as<br />a unit. When unset, the operator-level default applies. |  | Optional: \{\} <br /> |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#duration-v1-meta)_ | Interval is the reconciliation interval. Default 30m. | 30m | Optional: \{\} <br /> |
| `routing` _[TunnelRoutingSpec](#tunnelroutingspec)_ | Routing configures tunnel-wide originRequest defaults + the catch-all<br />default backend. The catch-all is auto-appended by the reconciler;<br />users only override it here when http_status:404 is wrong for them. |  | Optional: \{\} <br /> |

#### CloudflareTunnelStatus



CloudflareTunnelStatus is the observed state.



_Appears in:_
- [CloudflareTunnel](#cloudflaretunnel)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#condition-v1-meta) array_ | Conditions: Ready, ConnectorReady, RemoteConfigApplied, plus reason<br />strings drawn from internal/conventions/conditions.go. |  | Optional: \{\} <br /> |
| `phase` _[Phase](#phase)_ | Phase is a coarse summary derived from the Ready condition (Foundation §8). | Pending | Enum: [Ready Reconciling Error Pending] <br />Optional: \{\} <br /> |
| `tunnelID` _string_ | TunnelID is the Cloudflare-assigned UUID. |  | Optional: \{\} <br /> |
| `tunnelCNAME` _string_ | TunnelCNAME is <tunnelID>.cfargotunnel.com. Populated after create. |  | Optional: \{\} <br /> |
| `connectionsHealthy` _integer_ | ConnectionsHealthy is the count of active connectors observed via<br />GET /cfd_tunnel/\{id\}/connections. Zero is a meaningful value (no<br />healthy connectors yet) and is always serialized. |  | Optional: \{\} <br /> |
| `observedIngress` _[IngressEntrySnapshot](#ingressentrysnapshot) array_ | ObservedIngress is the materialized ingress list as last PUT to<br />/configurations. Used for drift detection — the reconciler skips a<br />PUT when the computed list matches this slice exactly. |  | Optional: \{\} <br /> |
| `observedDataplaneDeploymentHash` _string_ | ObservedDataplaneDeploymentHash is the sha256 of the last successfully<br />applied dataplane Deployment's SSA-relevant fields. ensureDataplane<br />skips the Apply when the computed hash matches. |  | Optional: \{\} <br /> |
| `observedDataplaneServiceHash` _string_ | ObservedDataplaneServiceHash is the analogous hash for the metrics Service. |  | Optional: \{\} <br /> |
| `attachedSources` _[AttachedSource](#attachedsource) array_ | AttachedSources lists every source object currently contributing to<br />this tunnel's ingress. Informational; the lexicographically-first entry<br />is the owner-reference target (or the original owner if still present). |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the .metadata.generation last reconciled. |  | Optional: \{\} <br /> |
| `lastSyncedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ | LastSyncedAt is the wall-clock time of the most recent successful<br />reconcile (drift check + remote-config PUT, even if a no-op). |  | Optional: \{\} <br /> |
| `lastOrphanedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ | LastOrphanedAt is the timestamp of the first reconcile that observed this<br />CR as orphaned (auto-created with no OwnerReferences and an empty<br />Status.AttachedSources). Self-delete fires only when a subsequent<br />reconcile observes the same state past the pending-deletion grace window<br />(60s). Cleared as soon as a source attaches or owner-transfer succeeds.<br />Operator-managed; user edits will be reverted on the next reconcile. |  | Optional: \{\} <br /> |
| `lastReconcileToken` _string_ | LastReconcileToken is the controller-owned ack of the most recent<br />cloudflare.io/reconcile-at annotation value the controller has<br />observed. The prelude in internal/reconcile.ForceReconcileRequested<br />compares this against the live annotation; mismatch forces a full<br />re-check this reconcile (bypassing the change-detection short-<br />circuit). The operator NEVER modifies the annotation itself — only<br />this status field — so admin force-triggers are not auto-cleared. |  | Optional: \{\} <br /> |



#### CloudflareZoneConfigSpec



CloudflareZoneConfigSpec defines the desired state of CloudflareZoneConfig.



_Appears in:_
- [CloudflareZoneConfig](#cloudflarezoneconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `zoneID` _string_ | ZoneID is the Cloudflare Zone ID.<br />Mutually exclusive with ZoneRef. |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `zoneRef` _[ZoneReference](#zonereference)_ | ZoneRef references a CloudflareZone resource in the same namespace.<br />The controller resolves the zone ID from the referenced resource's status.<br />Mutually exclusive with ZoneID. |  | Optional: \{\} <br /> |
| `cloudflare` _[CloudflareCredentialRef](#cloudflarecredentialref)_ | Cloudflare overrides the operator-level default credential (sourced<br />from the operator's CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID env,<br />chart-set from a Secret). Per Foundation §5 the token and accountID<br />are inherited or overridden as a unit. Omitted entirely → the<br />operator-level env default applies. |  | Optional: \{\} <br /> |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#duration-v1-meta)_ | Interval is the reconciliation interval. | 30m | Optional: \{\} <br /> |
| `ssl` _[SSLSettings](#sslsettings)_ | SSL defines SSL/TLS settings for the zone. |  | Optional: \{\} <br /> |
| `security` _[SecuritySettings](#securitysettings)_ | Security defines security settings for the zone. |  | Optional: \{\} <br /> |
| `performance` _[PerformanceSettings](#performancesettings)_ | Performance defines performance settings for the zone. |  | Optional: \{\} <br /> |
| `network` _[NetworkSettings](#networksettings)_ | Network defines network settings for the zone. |  | Optional: \{\} <br /> |
| `dns` _[DNSSettings](#dnssettings)_ | DNS defines DNS-related settings for the zone. |  | Optional: \{\} <br /> |
| `botManagement` _[BotManagementSettings](#botmanagementsettings)_ | BotManagement defines bot management settings for the zone. |  | Optional: \{\} <br /> |

#### CloudflareZoneConfigStatus



CloudflareZoneConfigStatus defines the observed state of CloudflareZoneConfig.



_Appears in:_
- [CloudflareZoneConfig](#cloudflarezoneconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#condition-v1-meta) array_ | Conditions represent the latest available observations of the resource's state. |  | Optional: \{\} <br /> |
| `phase` _[Phase](#phase)_ | Phase is a coarse summary of the reconciliation state.<br />See Phase for the enum values. | Pending | Enum: [Ready Reconciling Error Pending] <br />Optional: \{\} <br /> |
| `zoneID` _string_ | ZoneID is the resolved Cloudflare Zone ID, populated regardless of<br />whether the spec used zoneID or zoneRef. |  | Optional: \{\} <br /> |
| `appliedSpecHash` _string_ | AppliedSpecHash is a hash of the settings-relevant spec fields the last<br />time reconciliation successfully applied them. When the current hash<br />matches, the controller skips the per-setting API calls. |  | Optional: \{\} <br /> |
| `lastSyncedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ | LastSyncedAt is the last time the zone config was successfully synced. |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration is the most recently observed generation of the CR. |  | Optional: \{\} <br /> |
| `lastReconcileToken` _string_ | LastReconcileToken is the controller-owned ack of the most recent<br />cloudflare.io/reconcile-at annotation value the controller has<br />observed. The prelude in internal/reconcile.ForceReconcileRequested<br />compares this against the live annotation; mismatch forces a full<br />re-check this reconcile (bypassing the change-detection short-<br />circuit). The operator NEVER modifies the annotation itself — only<br />this status field — so admin force-triggers are not auto-cleared. |  | Optional: \{\} <br /> |

#### CloudflareZoneSpec



CloudflareZoneSpec defines the desired state of a Cloudflare Zone.



_Appears in:_
- [CloudflareZone](#cloudflarezone)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the domain name to onboard (e.g., "example.com"). |  | MinLength: 1 <br />Required: \{\} <br /> |
| `type` _string_ | Type is the zone type. "full" means Cloudflare is authoritative DNS;<br />"partial" is CNAME setup; "secondary" mirrors an upstream master.<br />Immutable after creation. | full | Enum: [full partial secondary] <br /> |
| `paused` _boolean_ | Paused indicates whether the zone is paused (not serving traffic through Cloudflare). |  | Optional: \{\} <br /> |
| `deletionPolicy` _string_ | DeletionPolicy controls what happens when the CR is deleted.<br />"Retain" (default) leaves the zone in Cloudflare; "Delete" removes it. | Retain | Enum: [Retain Delete] <br /> |
| `cloudflare` _[CloudflareCredentialRef](#cloudflarecredentialref)_ | Cloudflare overrides the operator-level default credential (sourced<br />from the operator's CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID env,<br />chart-set from a Secret). Per Foundation §5 the token and accountID<br />are inherited or overridden as a unit; CEL on this CRD must reject<br />setting only one. Omitted entirely → the operator-level env default<br />applies. |  | Optional: \{\} <br /> |
| `interval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#duration-v1-meta)_ | Interval is the reconciliation interval. | 30m | Optional: \{\} <br /> |

#### CloudflareZoneStatus



CloudflareZoneStatus defines the observed state of a CloudflareZone.



_Appears in:_
- [CloudflareZone](#cloudflarezone)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#condition-v1-meta) array_ |  |  | Optional: \{\} <br /> |
| `zoneID` _string_ |  |  | Optional: \{\} <br /> |
| `status` _string_ | Status is the zone status in Cloudflare (initializing, pending, active, moved). |  | Optional: \{\} <br /> |
| `nameServers` _string array_ |  |  | Optional: \{\} <br /> |
| `originalNameServers` _string array_ |  |  | Optional: \{\} <br /> |
| `originalRegistrar` _string_ |  |  | Optional: \{\} <br /> |
| `activatedOn` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ |  |  | Optional: \{\} <br /> |
| `lastSyncedAt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#time-v1-meta)_ |  |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ |  |  | Optional: \{\} <br /> |
| `phase` _[Phase](#phase)_ | Phase is a coarse summary derived from the Ready condition (Foundation §8). | Pending | Enum: [Ready Reconciling Error Pending] <br />Optional: \{\} <br /> |
| `lastReconcileToken` _string_ | LastReconcileToken is the controller-owned ack of the most recent<br />cloudflare.io/reconcile-at annotation value the controller has<br />observed. The prelude in internal/reconcile.ForceReconcileRequested<br />compares this against the live annotation; mismatch forces a full<br />re-check this reconcile (bypassing the change-detection short-<br />circuit). The operator NEVER modifies the annotation itself — only<br />this status field — so admin force-triggers are not auto-cleared. |  | Optional: \{\} <br /> |

#### ConnectorImage



ConnectorImage specifies the cloudflared container image.



_Appears in:_
- [ConnectorSpec](#connectorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repository` _string_ | Repository is the container image repository. | docker.io/cloudflare/cloudflared | Optional: \{\} <br /> |
| `tag` _string_ | Tag is the image tag. When empty, the operator's compile-time default<br />applies. Partial overrides (repository-only OR tag-only) preserve the<br />user's value and combine with the default for the unset half. |  | Optional: \{\} <br /> |

#### ConnectorSpec



ConnectorSpec configures the operator-managed cloudflared Deployment.



_Appears in:_
- [CloudflareTunnelSpec](#cloudflaretunnelspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `replicas` _integer_ | Replicas is the desired Pod count. Default 2. Range 1-25. No HPA. | 2 | Maximum: 25 <br />Minimum: 1 <br /> |
| `image` _[ConnectorImage](#connectorimage)_ | Image specifies the cloudflared container image. When omitted, the<br />operator uses a compile-time default (cloudflare/cloudflared:<pinned>). |  | Optional: \{\} <br /> |
| `protocol` _string_ | Protocol selects cloudflared's transport. auto\|http2\|quic. | auto | Enum: [auto http2 quic] <br /> |
| `logLevel` _string_ | LogLevel passes to cloudflared --loglevel. | info | Enum: [debug info warn error] <br /> |
| `gracePeriodSeconds` _integer_ | GracePeriodSeconds is the cloudflared --grace-period (seconds).<br />terminationGracePeriodSeconds on the Pod is set to GracePeriodSeconds+15. | 30 | Minimum: 0 <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#resourcerequirements-v1-core)_ | Resources are the container resource requests/limits. Defaults observe-<br />not-prescribe: 50m/64Mi requests, 200m/256Mi limits. |  | Optional: \{\} <br /> |
| `nodeSelector` _object (keys:string, values:string)_ | NodeSelector is a pass-through to the Pod spec. |  | Optional: \{\} <br /> |
| `tolerations` _[Toleration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#toleration-v1-core) array_ | Tolerations is a pass-through to the Pod spec. |  | Optional: \{\} <br /> |
| `affinity` _[Affinity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#affinity-v1-core)_ | Affinity is a pass-through to the Pod spec. |  | Optional: \{\} <br /> |
| `topologySpreadConstraints` _[TopologySpreadConstraint](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#topologyspreadconstraint-v1-core) array_ | TopologySpreadConstraints is a pass-through to the Pod spec. |  | Optional: \{\} <br /> |
| `originCASecretRef` _[SecretReference](#secretreference)_ | OriginCASecretRef, when set, mounts the referenced Secret at<br />/etc/cloudflared/ca/ in the cloudflared Pod and threads<br />originRequest.caPool: /etc/cloudflared/ca/<key> into ingress entries<br />when noTLSVerify is false. Use for self-signed in-cluster origin TLS. |  | Optional: \{\} <br /> |

#### DNSSettings



DNSSettings defines DNS-related zone settings.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cnameFlattening` _string_ | CNAMEFlattening controls how the zone resolves CNAME records.<br />flatten_at_root: only flatten the apex (default Cloudflare behavior).<br />flatten_all: flatten every CNAME.<br />flatten_none: never flatten. |  | Enum: [flatten_at_root flatten_all flatten_none] <br />Optional: \{\} <br /> |

#### IngressEntrySnapshot



IngressEntrySnapshot is a status-only snapshot of one materialized ingress
entry. NOT the source-of-truth shape — the reconciler computes ingress fresh
each loop. Used for drift detection and PUT-skip; the projection rules
must match internal/cloudflare/tunnel.go mapConfigurationGetResponse
byte-for-byte so live-config and want-config snapshots are byte-comparable.



_Appears in:_
- [CloudflareTunnelStatus](#cloudflaretunnelstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `hostname` _string_ | Hostname is the public hostname. |  | Optional: \{\} <br /> |
| `path` _string_ | Path is the optional path filter. |  | Optional: \{\} <br /> |
| `service` _string_ | Service is the cloudflared service URL (e.g. http://svc.ns:80). |  | Optional: \{\} <br /> |
| `originRequest` _[IngressSnapshotOriginRequest](#ingresssnapshotoriginrequest)_ | OriginRequest mirrors the per-entry originRequest block as last PUT. |  | Optional: \{\} <br /> |

#### IngressSnapshotOriginRequest



IngressSnapshotOriginRequest projects the per-entry originRequest block.
Conditional projection (mirrors mapConfigurationGetResponse):
  - NoTLSVerify projected only when true (unset-vs-explicit-false ambiguity is unavoidable).
  - OriginServerName projected only when non-empty.

At least one must be set or the parent IngressEntrySnapshot.OriginRequest stays nil.



_Appears in:_
- [IngressEntrySnapshot](#ingressentrysnapshot)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `originServerName` _string_ |  |  | Optional: \{\} <br /> |
| `noTLSVerify` _boolean_ |  |  | Optional: \{\} <br /> |

#### MinifySettings



MinifySettings defines minification settings for CSS, HTML, and JavaScript.



_Appears in:_
- [PerformanceSettings](#performancesettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `css` _string_ | CSS enables CSS minification. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `html` _string_ | HTML enables HTML minification. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `js` _string_ | JS enables JavaScript minification. |  | Enum: [on off] <br />Optional: \{\} <br /> |

#### NetworkSettings



NetworkSettings defines network settings for a Cloudflare zone.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ipv6` _string_ | IPv6 enables IPv6 support. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `websockets` _string_ | WebSockets enables WebSocket support. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `pseudoIPv4` _string_ | PseudoIPv4 controls Pseudo IPv4 behavior. |  | Enum: [off add_header overwrite_header] <br />Optional: \{\} <br /> |
| `ipGeolocation` _string_ | IPGeolocation enables IP geolocation. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `opportunisticOnion` _string_ | OpportunisticOnion enables onion routing. |  | Enum: [on off] <br />Optional: \{\} <br /> |

#### ObservedTXTPayload



ObservedTXTPayload mirrors the decoded RegistryPayload fields in the CR's
Status for user-visible diagnostics. The internal payload type lives in
internal/cloudflare/; this is the API-stable surface.



_Appears in:_
- [CloudflareDNSRecordStatus](#cloudflarednsrecordstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _integer_ | Version is the payload schema version (currently always 1). |  | Optional: \{\} <br /> |
| `kind` _string_ | Kind is the encoded owner kind ("CloudflareDNSRecord" in v2alpha1). |  | Optional: \{\} <br /> |
| `namespace` _string_ | Namespace is the encoded owner namespace. |  | Optional: \{\} <br /> |
| `name` _string_ | Name is the encoded owner name. |  | Optional: \{\} <br /> |
| `contentHash` _string_ | ContentHash is the SHA256 of the canonicalized spec.content at TXT<br />write time. Used by drift detection. |  | Optional: \{\} <br /> |
| `rawContent` _string_ | RawContent is the raw TXT content as received from Cloudflare when<br />decoding failed. Set instead of Version/Kind/Namespace/Name so users<br />can see what's there even when the operator can't parse it. |  | Optional: \{\} <br /> |
| `codec` _string_ | Codec reports which decoder ("plaintext", "aes-gcm", or<br />"unrecognized") produced this payload. |  | Optional: \{\} <br /> |

#### PerformanceSettings



PerformanceSettings defines performance settings for a Cloudflare zone.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `cacheLevel` _string_ | CacheLevel controls the cache level. |  | Enum: [aggressive basic simplified] <br />Optional: \{\} <br /> |
| `browserCacheTTL` _integer_ | BrowserCacheTTL is the browser cache TTL in seconds. 0 means respect existing headers. |  | Minimum: 0 <br />Optional: \{\} <br /> |
| `minify` _[MinifySettings](#minifysettings)_ | Minify controls minification settings. |  | Optional: \{\} <br /> |
| `polish` _string_ | Polish controls image optimization. |  | Enum: [off lossless lossy] <br />Optional: \{\} <br /> |
| `brotli` _string_ | Brotli enables brotli compression. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `earlyHints` _string_ | EarlyHints enables early hints. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `http2` _string_ | HTTP2 enables HTTP/2. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `http3` _string_ | HTTP3 enables HTTP/3. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `alwaysOnline` _string_ | AlwaysOnline serves cached pages when the origin is unreachable. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `rocketLoader` _string_ | RocketLoader defers JavaScript loading to improve perceived performance.<br />Cloudflare is sunsetting Rocket Loader; the field will be removed when<br />the API is retired. |  | Enum: [on off] <br />Optional: \{\} <br /> |

#### Phase

_Underlying type:_ _string_

Phase is reserved as the schema seat for the coarse-grained status summary
derived from the Ready condition. Specs 2/3 add `Phase` to each CRD's
status; Foundation declares the type and constants only.

_Validation:_
- Enum: [Ready Reconciling Error Pending]

_Appears in:_
- [CloudflareDNSRecordStatus](#cloudflarednsrecordstatus)
- [CloudflareRulesetStatus](#cloudflarerulesetstatus)
- [CloudflareTunnelStatus](#cloudflaretunnelstatus)
- [CloudflareZoneConfigStatus](#cloudflarezoneconfigstatus)
- [CloudflareZoneStatus](#cloudflarezonestatus)

| Field | Description |
| --- | --- |
| `Ready` |  |
| `Reconciling` |  |
| `Error` |  |
| `Pending` |  |

#### RecordMode

_Underlying type:_ _string_

RecordMode controls operator write behavior on a CloudflareDNSRecord.

_Validation:_
- Enum: [Managed Observe]

_Appears in:_
- [CloudflareDNSRecordSpec](#cloudflarednsrecordspec)

| Field | Description |
| --- | --- |
| `Managed` | RecordModeManaged is the default. The operator creates / updates /<br />deletes the underlying Cloudflare record and TXT companion as needed.<br /> |
| `Observe` | RecordModeObserve means the operator reads Cloudflare state and<br />populates Status, but never writes. Spec.Adopt has no effect. Useful<br />for verifying state before promoting to Managed (which would<br />otherwise refuse adoption without a matching TXT companion under<br />design §2 Q2's no-silent-backfill rule).<br /> |

#### RuleLogging



RuleLogging configures per-rule logging. Sibling of ActionParameters in
the Cloudflare API. Today exposes only the API's `enabled` flag; future
fields (sampling, destinations) extend this struct without rename.

Reconciliation note: omitting the logging block leaves Cloudflare's per-action
default in place. Set logging.enabled only when you want to override the
default for that action (e.g. enabled=true on `skip`, where logging is off
by default). Setting enabled=false explicitly will diff against the API on
every reconcile because Cloudflare's response shape can't distinguish that
case from "no logging configured".



_Appears in:_
- [RulesetRuleSpec](#rulesetrulespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled enables per-rule logging.<br />Note: due to Cloudflare API semantics, setting Enabled=false is<br />indistinguishable from omitting the Logging block entirely. The<br />operator normalizes both forms to "logging unset" on write to avoid<br />spurious drift loops. To enable logging, set true. |  | Optional: \{\} <br /> |

#### RulesetRuleSpec



RulesetRuleSpec defines a single rule within a Cloudflare Ruleset.



_Appears in:_
- [CloudflareRulesetSpec](#cloudflarerulesetspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `action` _string_ | Action is the action to perform when the rule matches. |  | Enum: [block challenge js_challenge managed_challenge log skip execute redirect rewrite route score serve_error set_cache_settings set_config compress_response force_connection_close] <br />Required: \{\} <br /> |
| `expression` _string_ | Expression is the filter expression for the rule. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `description` _string_ | Description is an informative description of the rule. |  | Optional: \{\} <br /> |
| `enabled` _boolean_ | Enabled indicates whether the rule is active. | true | Optional: \{\} <br /> |
| `actionParameters` _[JSON](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.30/#json-v1-apiextensions-k8s-io)_ | ActionParameters contains action-specific parameters as free-form JSON. |  | Type: object <br />Optional: \{\} <br /> |
| `logging` _[RuleLogging](#rulelogging)_ | Logging configures per-rule logging behavior. Sibling of ActionParameters<br />in the Cloudflare API; do not encode logging via ActionParameters. |  | Optional: \{\} <br /> |

#### SRVData



SRVData contains SRV-specific record fields.



_Appears in:_
- [CloudflareDNSRecordSpec](#cloudflarednsrecordspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `service` _string_ | Service is the symbolic service name (e.g., "_satisfactory", "_minecraft"). |  | Required: \{\} <br /> |
| `proto` _string_ | Proto is the transport protocol. |  | Enum: [_tcp _udp _tls] <br />Required: \{\} <br /> |
| `priority` _integer_ | Priority is the SRV priority (lower = preferred). |  | Maximum: 65535 <br />Minimum: 0 <br /> |
| `weight` _integer_ | Weight is the SRV weight for records with the same priority<br />(higher = more traffic). |  | Maximum: 65535 <br />Minimum: 0 <br /> |
| `port` _integer_ | Port is the TCP/UDP port the service listens on. |  | Maximum: 65535 <br />Minimum: 0 <br /> |
| `target` _string_ | Target is the canonical hostname of the machine providing the service. |  | Required: \{\} <br /> |

#### SSLSettings



SSLSettings defines SSL/TLS settings for a Cloudflare zone.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _string_ | Mode is the SSL mode. |  | Enum: [off flexible full strict] <br />Optional: \{\} <br /> |
| `minTLSVersion` _string_ | MinTLSVersion is the minimum TLS version. |  | Enum: [1.0 1.1 1.2 1.3] <br />Optional: \{\} <br /> |
| `tls13` _string_ | TLS13 controls TLS 1.3 setting. |  | Enum: [on off zrt] <br />Optional: \{\} <br /> |
| `alwaysUseHTTPS` _string_ | AlwaysUseHTTPS redirects all HTTP requests to HTTPS. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `automaticHTTPSRewrites` _string_ | AutomaticHTTPSRewrites rewrites HTTP URLs to HTTPS in page content. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `opportunisticEncryption` _string_ | OpportunisticEncryption enables opportunistic encryption. |  | Enum: [on off] <br />Optional: \{\} <br /> |

#### SecretReference



SecretReference identifies a Kubernetes Secret carrying credentials.



_Appears in:_
- [CloudflareCredentialRef](#cloudflarecredentialref)
- [ConnectorSpec](#connectorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `namespace` _string_ | Namespace of the Secret. Defaults to the referencing CR's namespace. |  | Optional: \{\} <br /> |
| `key` _string_ | Key inside the Secret holding the Cloudflare API token. Defaults to "token". | token | Optional: \{\} <br /> |

#### SecurityHeaderSettings



SecurityHeaderSettings models the zone-level HSTS / Strict-Transport-Security
setting (the strict_transport_security payload of the Cloudflare
security_header API). All fields are optional; nil fields are omitted from
the API call so individual flags can be toggled without re-asserting the rest.



_Appears in:_
- [SecuritySettings](#securitysettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled toggles HSTS for the zone. |  | Optional: \{\} <br /> |
| `maxAge` _integer_ | MaxAge is the HSTS max-age in seconds. |  | Maximum: 3.1536e+07 <br />Minimum: 0 <br />Optional: \{\} <br /> |
| `includeSubdomains` _boolean_ | IncludeSubdomains extends HSTS to subdomains. |  | Optional: \{\} <br /> |
| `preload` _boolean_ | Preload requests inclusion in browser HSTS preload lists. |  | Optional: \{\} <br /> |
| `nosniff` _boolean_ | Nosniff enables the X-Content-Type-Options: nosniff response header. |  | Optional: \{\} <br /> |

#### SecuritySettings



SecuritySettings defines security settings for a Cloudflare zone.



_Appears in:_
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `securityLevel` _string_ | SecurityLevel controls the security level. |  | Enum: [essentially_off low medium high under_attack] <br />Optional: \{\} <br /> |
| `challengeTTL` _integer_ | ChallengeTTL is the challenge TTL in seconds. |  | Enum: [300 900 1800 2700 3600 7200 10800 14400 28800 57600 86400] <br />Optional: \{\} <br /> |
| `browserCheck` _string_ | BrowserCheck enables browser integrity check. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `emailObfuscation` _string_ | EmailObfuscation enables email obfuscation. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `securityHeader` _[SecurityHeaderSettings](#securityheadersettings)_ | SecurityHeader configures the zone's HSTS / Strict-Transport-Security header. |  | Optional: \{\} <br /> |
| `serverSideExclude` _string_ | ServerSideExclude hides sensitive content from suspicious visitors. |  | Enum: [on off] <br />Optional: \{\} <br /> |
| `hotlinkProtection` _string_ | HotlinkProtection blocks hotlinking of images. |  | Enum: [on off] <br />Optional: \{\} <br /> |

#### TunnelFallback



TunnelFallback is the catch-all backend. Discriminated union: exactly one of
URL or HTTPStatus must be set. Enforced via CEL on the parent CRD.



_Appears in:_
- [TunnelRoutingSpec](#tunnelroutingspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `url` _string_ | URL is a full URL backend (e.g. "http://default.svc.cluster.local"). |  | Optional: \{\} <br /> |
| `httpStatus` _integer_ | HTTPStatus is a synthetic status backend (e.g. 404, 503). |  | Optional: \{\} <br /> |

#### TunnelOriginRequest



TunnelOriginRequest mirrors cloudflared's originRequest block at the
tunnel level (defaults inherited by every ingress entry that does not
supply its own via per-source annotations). Per-ingress overrides come
from cloudflare.io/origin-server-name and cloudflare.io/no-tls-verify
on the source Gateway / HTTPRoute / TLSRoute / Service.



_Appears in:_
- [TunnelRoutingSpec](#tunnelroutingspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `noTLSVerify` _boolean_ | NoTLSVerify disables TLS verification to the origin. |  | Optional: \{\} <br /> |
| `originServerName` _string_ | OriginServerName is the expected SAN on the origin certificate. |  | Optional: \{\} <br /> |

#### TunnelRoutingSpec



TunnelRoutingSpec configures tunnel-wide routing defaults.



_Appears in:_
- [CloudflareTunnelSpec](#cloudflaretunnelspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fallback` _[TunnelFallback](#tunnelfallback)_ | Fallback handles traffic that no synthesized ingress entry matches.<br />Omit to fall through to the auto-appended http_status:404. |  | Optional: \{\} <br /> |
| `originRequest` _[TunnelOriginRequest](#tunneloriginrequest)_ | OriginRequest defaults applied to all synthesized rules unless overridden<br />by per-source annotations (no-tls-verify, origin-server-name, …). |  | Optional: \{\} <br /> |

#### ZoneReference



ZoneReference selects a CloudflareZone CR by name (and optional namespace).
Used XOR with a literal zoneID per Foundation §7.



_Appears in:_
- [CloudflareDNSRecordSpec](#cloudflarednsrecordspec)
- [CloudflareRulesetSpec](#cloudflarerulesetspec)
- [CloudflareZoneConfigSpec](#cloudflarezoneconfigspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the CloudflareZone CR. |  | MinLength: 1 <br />Required: \{\} <br /> |
| `namespace` _string_ | Namespace of the CloudflareZone CR. Defaults to the referencing CR's namespace. |  | Optional: \{\} <br /> |



