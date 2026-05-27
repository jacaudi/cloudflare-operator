# Changelog

## [0.19.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.19.0...v0.19.1) (2026-05-27)

### Bug Fixes

* trigger release for queued dependency upgrades ([a927465](https://github.com/jacaudi/cloudflare-operator/commit/a927465867f302b8c381c930e7e90c010cceebf9))

## [0.19.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.18.3...v0.19.0) (2026-05-22)

* refactor(api)!: bump CRD API version cloudflare.io/v1alpha1 -> v2alpha1 ([89c7cf7](https://github.com/jacaudi/cloudflare-operator/commit/89c7cf7afadbb603d513167fb497c0aef3f768cb))


### Bug Fixes

* **api:** correct Spec.Adopt godoc + regenerate CRD manifests ([71191b9](https://github.com/jacaudi/cloudflare-operator/commit/71191b9e7b9bca959c2c15a83005a334170bb208))
* **api:** drop redundant field-level Enum marker on Spec.Mode ([8666865](https://github.com/jacaudi/cloudflare-operator/commit/8666865afa3134eaa65f6cb452088e6726485198))
* **bootstrap:** change-detection gate on terminal Status().Update (MIN-20) ([36e337c](https://github.com/jacaudi/cloudflare-operator/commit/36e337cd104b7ef3eddcd113db351edc48411d0a))
* **chart:** bind RBAC + --operator-namespace to .Release.Namespace; guard creds/namespace ([999862b](https://github.com/jacaudi/cloudflare-operator/commit/999862b6cb88cbb43b5834764ea830652c4672fc))
* **chart:** grant tunnel controller Gateway-API/events/services/secrets RBAC ([404d313](https://github.com/jacaudi/cloudflare-operator/commit/404d3132c31c47350838ca701c6b2de39c5d220d))
* **chart:** name the meta-operator Deployment just cloudflare-operator ([f806acf](https://github.com/jacaudi/cloudflare-operator/commit/f806acfd2d4d6047a80722294abed1332b20b328))
* **chart:** set automountServiceAccountToken: true (bjw-s common v5 no longer defaults it on; meta-operator crashlooped on missing SA token) ([466a55a](https://github.com/jacaudi/cloudflare-operator/commit/466a55a40a70f6179164705ff74e9765cbf42596))
* **cloudflared:** pin to 2026.5.0 + Renovate-track the const (Bug B) ([37ec709](https://github.com/jacaudi/cloudflare-operator/commit/37ec70970e030c559013c66ab2e37cd774d13356))
* **cmd:** surface logger Build error; structured fatal logging (MIN-3) ([49cc3a7](https://github.com/jacaudi/cloudflare-operator/commit/49cc3a7b9696b16d4d7437366c04072a704361f9))
* **envtest:** align HTTPRoute + TLSRoute chain-content tests with Bug D semantics ([ce4a029](https://github.com/jacaudi/cloudflare-operator/commit/ce4a0291b2ed59c008fc4ca5ccf72b06b7926a37))
* green-light PR [#127](https://github.com/jacaudi/cloudflare-operator/issues/127) CI (envtest CRD path + lint warnings) ([0313e3a](https://github.com/jacaudi/cloudflare-operator/commit/0313e3a9cf54c767c6febc6d9b417fa14193a0cd))
* **make:** wire test target to a manifests dep so bin/crd-staging exists ([9460b9d](https://github.com/jacaudi/cloudflare-operator/commit/9460b9d8c9788c5bf952f24cfd2bf9e43009e35d))
* **manager:** init zap logger before flag-parse so errors are structured ([f4d770c](https://github.com/jacaudi/cloudflare-operator/commit/f4d770cbcdf4404d8c7de3e8e4c8a4feb83eeb84))
* **meta:** clamp replicas <1 to 1; lock enabled->disabled drift-correction ([1fb5249](https://github.com/jacaudi/cloudflare-operator/commit/1fb5249fe0949ff290318a30f5b0e98bd6cf2ed0))
* **tunnel/attach:** make owner-transfer Patch genuinely optimistic-locked ([831e468](https://github.com/jacaudi/cloudflare-operator/commit/831e46849df751f4d2cdd34f13c8c2c959622c5b))
* **tunnel:** cascadeGCEligible = auto-created OR source labels (Bug E part 1) ([49dde67](https://github.com/jacaudi/cloudflare-operator/commit/49dde6781a695735f56b035c3ca9ac6c432663ab))
* **tunnel:** collapse HTTP+HTTPS sibling listeners to one HTTPS contribution; surface gateway Defaults (Bug 1) ([2683ab0](https://github.com/jacaudi/cloudflare-operator/commit/2683ab089625f65f8df6902b8b180f8a1d55ee13))
* **tunnel:** condition-DeepEqual gate writeParentStatus (simplify F) ([4059fe7](https://github.com/jacaudi/cloudflare-operator/commit/4059fe7deb082cc9dcf3c125ea6429a8ff18fbb8))
* **tunnel:** derive HTTPRoute origin scheme from parent listener + surface Defaults (Bug 2) ([2792833](https://github.com/jacaudi/cloudflare-operator/commit/2792833063c73afd9de5fc7fedb3821daafd7d94))
* **tunnel:** deterministic firstListenerHostname ordering ([09863e0](https://github.com/jacaudi/cloudflare-operator/commit/09863e0068ed99170ed85c8848b503b16dc909b5))
* **tunnel:** enqueue all routes in namespace on tunnel change (simplify A) ([c6fa333](https://github.com/jacaudi/cloudflare-operator/commit/c6fa3336b3740e3fd8af5a8c9c7d94443a824dc5))
* **tunnel:** gate owner-transfer on isAutoCreated (restore design §7) ([b7e62fa](https://github.com/jacaudi/cloudflare-operator/commit/b7e62fa9e66fcd7558118ea8e0cc33815199b7ad))
* **tunnel:** Gateway DNS desired set from HTTP(S) contribs, not listener hostnames (IMP-2) ([64fc51c](https://github.com/jacaudi/cloudflare-operator/commit/64fc51ce873a919e3f41d9dfa4489a5b888ddf13))
* **tunnel:** Gateway/HTTPRoute/TLSRoute source .Owns(CloudflareDNSRecord) (S2 / [#1](https://github.com/jacaudi/cloudflare-operator/issues/1)d) ([7e7366b](https://github.com/jacaudi/cloudflare-operator/commit/7e7366be3bd5dd458c6dc521e58214d5ed0bbc89)), closes [#1d](https://github.com/jacaudi/cloudflare-operator/issues/1d)
* **tunnel:** gracefully skip TLSRoute source when its CRD is absent ([d346051](https://github.com/jacaudi/cloudflare-operator/commit/d346051ed4c2b8fe368db65a433d0140ae3b95b9))
* **tunnel:** HTTPRoute no-hostname reports NoListenerHostname, not IncompatibleFilters (MIN-16) ([c60830e](https://github.com/jacaudi/cloudflare-operator/commit/c60830ee2381a0c48d49485b8383caaf4363a5d4))
* **tunnel:** include TLS-listener apex hostnames in Gateway DNS desired set (IMP-2 corrective) ([5ee4bf6](https://github.com/jacaudi/cloudflare-operator/commit/5ee4bf6b8b9a20fa72d2d438e901f9bb297ad667))
* **tunnel:** requeue on Status().Update conflict before self-delete (MIN-15) ([7bb36aa](https://github.com/jacaudi/cloudflare-operator/commit/7bb36aa78a18b99c80306fcea889087e7bb52dcc))
* **tunnel:** route chain uses chainContentFor; block wildcard-only w/o gateway-apex (Bug D) ([8082d5f](https://github.com/jacaudi/cloudflare-operator/commit/8082d5fe02acc49b21b621e5d211f46560176b0d))
* **tunnel:** ServiceSource .Owns(CloudflareDNSRecord) — re-emit on out-of-band delete (S2 / [#1](https://github.com/jacaudi/cloudflare-operator/issues/1)d) ([eb62dc2](https://github.com/jacaudi/cloudflare-operator/commit/eb62dc209e6a0c4c27bbba78f6ce2e88707fa917)), closes [#1d](https://github.com/jacaudi/cloudflare-operator/issues/1d)
* **tunnel:** splitImage keeps registry port when no tag present (MIN-5) ([debd91c](https://github.com/jacaudi/cloudflare-operator/commit/debd91c961611bf1750025e3c6da031460062245))
* **tunnel:** stamp-on-detect self-heal + cascadeGCEligible gate (Bug E part 3) ([d9e14b7](https://github.com/jacaudi/cloudflare-operator/commit/d9e14b722fd89599888785165599d0bc8b01b493))
* **txt-registry:** AffixName prefix-as-leftmost-label so companion ends in zone (S1; external-jacaudi 81058 root cause) ([e1ef6ae](https://github.com/jacaudi/cloudflare-operator/commit/e1ef6ae1e4315082d7e9c89480002445abb1cc4b))
* **txt-registry:** canonicalize TXT content at mapRecordResponse SDK boundary (Bug A) ([02be0b7](https://github.com/jacaudi/cloudflare-operator/commit/02be0b7941e22cd2ffce87fa97689011e791cb5f))
* **txt-registry:** encode TXT content to RFC1035 presentation form on write (Bug A) ([cf3fd25](https://github.com/jacaudi/cloudflare-operator/commit/cf3fd2551dee5e6e821c95b43d6a78b5aaf0de27))
* **txt-registry:** sanitize wildcard label in AffixName companion name (Bug C) ([7e39b4e](https://github.com/jacaudi/cloudflare-operator/commit/7e39b4e96b774d97a18752bece424e3feda007c1))
* **zone/dnsrecord:** retry missing TXT companion without content drift ([931a630](https://github.com/jacaudi/cloudflare-operator/commit/931a630dc9d24f63fe17cf6a43d68bbe95e9e145))
* **zone:** single composed companion reconcile; gate Ready on ownershipOK; 81053 primary relist (S1 a/b/c) ([4b0bcbf](https://github.com/jacaudi/cloudflare-operator/commit/4b0bcbf365867c3060c5f89b4aa8e26391749088))


### Features

* **api,reconcile:** StatusEpilogue interface + adapters on 5 Status types (simplify D, prep) ([afc43a6](https://github.com/jacaudi/cloudflare-operator/commit/afc43a6cb9a0c585015540f713677299c7ed2cde))
* **api:** add CloudflareTunnelStatus.LastOrphanedAt ([1229a17](https://github.com/jacaudi/cloudflare-operator/commit/1229a1721fa8ebf43ec4488ac800f4d3a7dfd750))
* **api:** add LastReconcileToken status field to all 5 CRDs (S6 / [#2](https://github.com/jacaudi/cloudflare-operator/issues/2)) ([18fadfe](https://github.com/jacaudi/cloudflare-operator/commit/18fadfec43d4bda12999a4d1a348f6613032918f))
* **api:** add LegacyCompanionGCDone status field on CloudflareDNSRecord (simplify B) ([ab2fd70](https://github.com/jacaudi/cloudflare-operator/commit/ab2fd70e053399551113244aa7bf743d0a16363a))
* **api:** CloudflareDNSRecord.Status TXT-registry + observe fields ([687ea11](https://github.com/jacaudi/cloudflare-operator/commit/687ea11565a104f400ee241f20302557f1fb55f2))
* **api:** RecordMode enum + CloudflareDNSRecord.Spec.Mode field ([84b9b0f](https://github.com/jacaudi/cloudflare-operator/commit/84b9b0f6245707092d9e7222a4b024a1397a57aa))
* **api:** trim TunnelOriginRequest to 2 fields; add IngressSnapshotOriginRequest ([bb28975](https://github.com/jacaudi/cloudflare-operator/commit/bb2897523778ecb3ae0358f33c6ad3350a65518f))
* **chart:** controllers.tunnel.connector.image facade (Bug B) ([ff2e93c](https://github.com/jacaudi/cloudflare-operator/commit/ff2e93cb9c11f17b9b410a4406fd624effff114e))
* **chart:** controllers.tunnel.connector.resources facade (opt-in) ([a4e960d](https://github.com/jacaudi/cloudflare-operator/commit/a4e960d8a03ad547f8d059a4b04568d3f4ff6ecd))
* **chart:** expose meta-operator replicas facade (default 1; HA via leaderElection) ([8b5e448](https://github.com/jacaudi/cloudflare-operator/commit/8b5e448f73f6245be3a74da3cba3c533091f8a1d))
* **cloudflare:** aesCodec for TXT registry (AES-256-GCM) ([4ded106](https://github.com/jacaudi/cloudflare-operator/commit/4ded1069b154a17e82161812d38bbf91331d9444))
* **cloudflare:** autoDetectingCodec for read-side dispatch ([9384fd5](https://github.com/jacaudi/cloudflare-operator/commit/9384fd5e20059f68a89ddb7316e649b5bfc76182))
* **cloudflare:** LRU-cache *cfgo.Client by (token, accountID) (simplify E) ([37615ff](https://github.com/jacaudi/cloudflare-operator/commit/37615ff97f40d02bc76581ffbcbe5cb3b54ff6ab))
* **cloudflare:** plaintextCodec for TXT registry ([08a126e](https://github.com/jacaudi/cloudflare-operator/commit/08a126e7ebc7352935b9416935e4f635f0756fe3))
* **cloudflare:** TXT registry foundational types ([f70614d](https://github.com/jacaudi/cloudflare-operator/commit/f70614d5b1684d3bdc6239d2fcf447b91df4eb99))
* **cmd:** build-version injection + --version flag (MIN-4) ([d778e8d](https://github.com/jacaudi/cloudflare-operator/commit/d778e8d952d31ad35dcee7ae43bd3bed25b6b9e3))
* **controllers:** wire Feature F force-reconcile prelude into all 5 CRD controllers (S6 / [#2](https://github.com/jacaudi/cloudflare-operator/issues/2)) ([76fa671](https://github.com/jacaudi/cloudflare-operator/commit/76fa6715d86f494224b75483bbb9251f6aabb460))
* **conventions:** add AnnotationAutoCreated for cascade-GC scoping ([2c978a7](https://github.com/jacaudi/cloudflare-operator/commit/2c978a72b6b28ee3fea2f3abac7c022192ca80db))
* **conventions:** add ReasonOriginRequestWiped for one-shot wipe events ([38d906f](https://github.com/jacaudi/cloudflare-operator/commit/38d906fe06951d631cd2157b4804564ab6f4a6b0))
* **conventions:** add ReasonOwnershipCompanionFailed (S1 sub-bug c) ([2006242](https://github.com/jacaudi/cloudflare-operator/commit/2006242f99ae0dac285281e7cf576e3aaa64588e))
* **conventions:** add ReasonTerminalNoSources for cascade-GC self-delete ([0dcdbe0](https://github.com/jacaudi/cloudflare-operator/commit/0dcdbe06da710c2373b441255a25bcbb58529030))
* **conventions:** SafeRecorder nil-safe EventRecorder wrapper (simplify L) ([c77c798](https://github.com/jacaudi/cloudflare-operator/commit/c77c798e99ca549540158990d456e151a53da771))
* **conventions:** TXT-registry + observe-mode zone reasons ([c11e882](https://github.com/jacaudi/cloudflare-operator/commit/c11e88228d359a050b714b5e0926080109423f80))
* **foundation:** meta-operator scaffold + 6 CRDs + shared packages ([#112](https://github.com/jacaudi/cloudflare-operator/issues/112)) ([3d8ca86](https://github.com/jacaudi/cloudflare-operator/commit/3d8ca8615a18bb90de1201e85faa72feae153021))
* **manager:** label-scope Secret cache to app.kubernetes.io/part-of=cloudflare-operator (simplify C) ([10de9e5](https://github.com/jacaudi/cloudflare-operator/commit/10de9e564378a2113380cedda4958de958308ada))
* **meta:** carry tunnel connector-resources JSON through bootstrap.Config ([c44b88a](https://github.com/jacaudi/cloudflare-operator/commit/c44b88a76c2f25839d9c5865e0ac7f12f574e010))
* **meta:** inject connector-resources env on the tunnel controller Deployment only ([8ec4cda](https://github.com/jacaudi/cloudflare-operator/commit/8ec4cda0086ce373e03eadf5c553156f7921da5e))
* **reconcile:** ForceReconcileRequested prelude helper + reconcile-at annotation (S6 / [#2](https://github.com/jacaudi/cloudflare-operator/issues/2)) ([8d5749b](https://github.com/jacaudi/cloudflare-operator/commit/8d5749b37f1c07a829de1597518abae16c13386c))
* **reconcile:** HasSourceLabels boolean (all-three) for cascade-GC eligibility (Bug E) ([c482115](https://github.com/jacaudi/cloudflare-operator/commit/c482115ad5151953826e491846d210c3f2f335b6))
* **reconcile:** ShouldMutate helper for observe/read-only modes ([da18e8f](https://github.com/jacaudi/cloudflare-operator/commit/da18e8f6d63786b7c1465f128e173c433fed068b))
* **reconcile:** structured error-class helper (S6 / [#7](https://github.com/jacaudi/cloudflare-operator/issues/7)) ([e0dda8d](https://github.com/jacaudi/cloudflare-operator/commit/e0dda8d683834eb232bf229447428163c0aa5b6b))
* **reconcile:** UpdateStatusIfChanged[T] generic helper (simplify D, helper) ([c8ee89e](https://github.com/jacaudi/cloudflare-operator/commit/c8ee89edcb06208da28881978861d847f6e2e98a))
* **tunnel:** --tunnel-connector-image flag + Config + parseConnectorImage (Bug B) ([c86cf23](https://github.com/jacaudi/cloudflare-operator/commit/c86cf234b3f62d5ee4272e228cabc849ea018b86))
* **tunnel/attach:** isAutoCreated predicate ([1a0b001](https://github.com/jacaudi/cloudflare-operator/commit/1a0b0010e400e2362fbd3696fbc2f99b5c077b34))
* **tunnel/attach:** needsOwnerTransfer and isOrphaned predicates ([e3b1051](https://github.com/jacaudi/cloudflare-operator/commit/e3b1051619aed63baedd0f24ff5fc0aafce1e219))
* **tunnel/attach:** stamp cloudflare.io/auto-created on new CRs ([383f863](https://github.com/jacaudi/cloudflare-operator/commit/383f863f8d79717b3cd713417a2f3f754f0df73f))
* **tunnel/attach:** TransferOwnershipIfNeeded ([43eb648](https://github.com/jacaudi/cloudflare-operator/commit/43eb6488db9aab2a77aca57b827fdb2afed2f85c))
* **tunnel/httproute:** inherit cloudflare.io/* annotations from parent Gateway ([345ccf0](https://github.com/jacaudi/cloudflare-operator/commit/345ccf01deeb49e4b47bb072b5579f0bfa298d4b))
* **tunnel/tlsroute:** inherit cloudflare.io/* annotations from parent Gateway ([f7ab97f](https://github.com/jacaudi/cloudflare-operator/commit/f7ab97f5347d26c892c36973fc2f4f1da1176c26))
* **tunnel:** add defaultsFromAnnotations + extend inherited keys with scheme (Bug 2) ([2a8cbaa](https://github.com/jacaudi/cloudflare-operator/commit/2a8cbaa1b099fcfa494edab0717fd8654230cd79))
* **tunnel:** annotation-inheritance helper for routes ← Gateway ([9098576](https://github.com/jacaudi/cloudflare-operator/commit/90985764efe211aaf5f8125312eb4f64d67051a9))
* **tunnel:** cascade-GC orphan-state management in Reconcile ([e91c48e](https://github.com/jacaudi/cloudflare-operator/commit/e91c48e990c782c0cdc8d4e587516ce22316803c))
* **tunnel:** chainContentFor + cloudflare.io/gateway-apex constants (Bug D) ([8441600](https://github.com/jacaudi/cloudflare-operator/commit/8441600ef28938e67d55dc7f199d735c89b59f92))
* **tunnel:** cloudflare.io/zone-ref-namespace for cross-namespace zone refs ([f07a10f](https://github.com/jacaudi/cloudflare-operator/commit/f07a10f65287696145fc9844ab9f7712cc247462))
* **tunnel:** consolidated EmitDNSRecord helper via SSA ([c2068e7](https://github.com/jacaudi/cloudflare-operator/commit/c2068e74ac69f2c53612b21053ad204f7110841d))
* **tunnel:** defaultsFromAnnotations takes spec-level fallback ([d2f2646](https://github.com/jacaudi/cloudflare-operator/commit/d2f26460cdf6b58447399dfa494747c42cbdf1a8))
* **tunnel:** eventDedupe type for suppressing repeated source-reconciler Events ([3f1af83](https://github.com/jacaudi/cloudflare-operator/commit/3f1af835a9505a07211d9fd1c00f9fe4a681048e))
* **tunnel:** gateway-source single-apex on valid gateway-apex override (Bug D) ([b59fb68](https://github.com/jacaudi/cloudflare-operator/commit/b59fb68a6494e7ab9803c0f6b417a226f3c185f8))
* **tunnel:** inject CLOUDFLARE_TUNNEL_CONNECTOR_IMAGE on the tunnel bundle (Bug B) ([2edfeee](https://github.com/jacaudi/cloudflare-operator/commit/2edfeee60b0037510c64c1492e1525516a57f40f))
* **tunnel:** one-shot OriginRequestWiped warning event before PUT ([ec5b9e0](https://github.com/jacaudi/cloudflare-operator/commit/ec5b9e01c3983021cd1a248af78e38f003394fec))
* **tunnel:** pruneOrphanedDNSRecords helper for hostname-set drift ([34a0398](https://github.com/jacaudi/cloudflare-operator/commit/34a0398ec2122c6e97c21b9105aec4c8e6fa96da))
* **tunnel:** pruneOrphanedDNSRecords self-migrates old-form emitted CRs (S4 / [#6](https://github.com/jacaudi/cloudflare-operator/issues/6)) ([64fde72](https://github.com/jacaudi/cloudflare-operator/commit/64fde72a743b1b7bf460be4820c3631c75b009e6))
* **tunnel:** runTunnel layers chart image over the pin into Options.DefaultImage (Bug B) ([69158a1](https://github.com/jacaudi/cloudflare-operator/commit/69158a1dbe2ef7c8b2cff710f94c9eba9abf8698))
* **tunnel:** seed DefaultConnector.Resources from CLOUDFLARE_TUNNEL_CONNECTOR_RESOURCES ([c955bc4](https://github.com/jacaudi/cloudflare-operator/commit/c955bc449dfa795b5d4d7a68ec9b1d671050e6c5))
* **tunnel:** snapshotFromConfig projects OriginRequest symmetrically ([3ca2f91](https://github.com/jacaudi/cloudflare-operator/commit/3ca2f91af3525cce5b54d7d1551c9e8adc19641a))
* **tunnel:** surface orphaned-but-unmanaged tunnels (Event+condition), never delete (Bug E part 2) ([05757be](https://github.com/jacaudi/cloudflare-operator/commit/05757be046a6ecb85ff8a6c047322fed29fb2047))
* **tunnelsynth:** DefaultsFor builds synth Defaults from a CloudflareTunnel CR ([f16bc47](https://github.com/jacaudi/cloudflare-operator/commit/f16bc47f8b0d8e808397cdec85712a7e12933bce))
* **tunnel:** tunnel controller bundle (Phase 3 of total refactor) ([#115](https://github.com/jacaudi/cloudflare-operator/issues/115)) ([e5daa52](https://github.com/jacaudi/cloudflare-operator/commit/e5daa52f11ffed789a62b913a70ff7531c13260c)), closes [#3](https://github.com/jacaudi/cloudflare-operator/issues/3) [#9](https://github.com/jacaudi/cloudflare-operator/issues/9) [#10](https://github.com/jacaudi/cloudflare-operator/issues/10) [#14](https://github.com/jacaudi/cloudflare-operator/issues/14) [#10](https://github.com/jacaudi/cloudflare-operator/issues/10)
* **tunnel:** wire defaultsFromAnnotations + DefaultsFor into Service path ([824f413](https://github.com/jacaudi/cloudflare-operator/commit/824f413170dc9169e2973849860bc5840ea651c5))
* **tunnel:** wire defaultsFromAnnotations + DefaultsFor into TLSRoute path ([7896b69](https://github.com/jacaudi/cloudflare-operator/commit/7896b699b16eb1cdafcbd71a71221b04c8a1bcf3))
* **tunnel:** wire eventDedupe into the 4 source reconcilers ([56b28ef](https://github.com/jacaudi/cloudflare-operator/commit/56b28ef34ca1d6ec2ae7df72f01d3003c45b6a4b))
* **tunnel:** wire GetConfiguration for out-of-band drift detection ([1ecd92d](https://github.com/jacaudi/cloudflare-operator/commit/1ecd92dd900ff7fb3bb253d78a63cb64759a07f2))
* **tunnel:** wire owner-transfer into Reconcile (early) ([24afa84](https://github.com/jacaudi/cloudflare-operator/commit/24afa84ea975971a8a19bc19acfed91349a619b8))
* **tunnel:** wire proxied+ttl annotations into emitted Spec; default proxied=true (S3 / [#4](https://github.com/jacaudi/cloudflare-operator/issues/4)) ([444d622](https://github.com/jacaudi/cloudflare-operator/commit/444d622d61b088a3b0005f1d148111d22b6b2064))
* **tunnel:** wire pruneOrphanedDNSRecords into all 4 source reconcilers ([8f88e55](https://github.com/jacaudi/cloudflare-operator/commit/8f88e553c28d66ff0f421eca4bb766abe824aa49))
* **txt-registry:** add CanonicalizeTXT RFC1035 presentation->logical (Bug A) ([7b95029](https://github.com/jacaudi/cloudflare-operator/commit/7b95029b53b8942bb7c2377db60e42c0e8671f75))
* **txt-registry:** add EncodeTXT logical->RFC1035 presentation (Bug A write side) ([9abc482](https://github.com/jacaudi/cloudflare-operator/commit/9abc4827d1dbf6b3eab79c4640f516f74807db8f))
* **zone/dnsrecord:** adopt-with-TXT-verification (no silent backfill) ([a3749c7](https://github.com/jacaudi/cloudflare-operator/commit/a3749c7189622fbcb5bac63b2a13c7d1b857290c))
* **zone/dnsrecord:** cascade TXT-companion delete on record delete ([93eb38a](https://github.com/jacaudi/cloudflare-operator/commit/93eb38a4f6c9c29d1bfbe9a047a847153106a66d))
* **zone/dnsrecord:** wire observe-mode early-exit (zero writes) ([95a9d42](https://github.com/jacaudi/cloudflare-operator/commit/95a9d42090b277ad1e1267b76b08bd94ae291e0f))
* **zone/dnsrecord:** wire TXT codec build + key-unavailable halt ([8ebab4b](https://github.com/jacaudi/cloudflare-operator/commit/8ebab4bb8f4078c861fad18181a7524b1401ddf2))
* **zone/dnsrecord:** write/refresh TXT companion on create+update ([57a69fb](https://github.com/jacaudi/cloudflare-operator/commit/57a69fb5fe2c7a8b1b7cd2991b8c401106cb1444))
* **zone:** best-effort provably-own legacy TXT companion self-GC (S1 D4) ([e834c63](https://github.com/jacaudi/cloudflare-operator/commit/e834c6394d2e75a75fe3940e5a85a3d689297795))
* **zone:** composed reconcileTXTCompanion (ID-first + list + classify + 81058-relist) (S1 a/b) ([c7a8039](https://github.com/jacaudi/cloudflare-operator/commit/c7a8039bad5b4a259616db52e2ae61acebda90b1))
* **zone:** gate gcLegacyCompanion on Status.LegacyCompanionGCDone (simplify B) ([3563bf1](https://github.com/jacaudi/cloudflare-operator/commit/3563bf1de38fd309b7acf0f3f11ba54c7d52472e))
* **zone:** TXT-registry reconciler-side orchestration ([3c27c1e](https://github.com/jacaudi/cloudflare-operator/commit/3c27c1ecbff2888569b7a1fcb0adb1de8077b0d3))
* **zone:** zone controller bundle (Phase 2 of total refactor) ([#114](https://github.com/jacaudi/cloudflare-operator/issues/114)) ([402b494](https://github.com/jacaudi/cloudflare-operator/commit/402b494870f2c2e6ae47f6d979626971d211844e)), closes [#3](https://github.com/jacaudi/cloudflare-operator/issues/3) [#4](https://github.com/jacaudi/cloudflare-operator/issues/4) [#1](https://github.com/jacaudi/cloudflare-operator/issues/1) [#3](https://github.com/jacaudi/cloudflare-operator/issues/3) [#4](https://github.com/jacaudi/cloudflare-operator/issues/4) [#5](https://github.com/jacaudi/cloudflare-operator/issues/5) [#3](https://github.com/jacaudi/cloudflare-operator/issues/3) [#6](https://github.com/jacaudi/cloudflare-operator/issues/6) [hi#coverage](https://github.com/hi/issues/coverage)


### Performance Improvements

* **tunnel:** field indexers for Gateway→Route MapFuncs ([c83704a](https://github.com/jacaudi/cloudflare-operator/commit/c83704a6f023d6605eda3d5578de59b5dff2907f))
* **tunnel:** hash-gate ensureDataplane SSA patches (simplify H) ([5a1b5da](https://github.com/jacaudi/cloudflare-operator/commit/5a1b5da180a707e216a107dbc660c070028e12e3))
* **tunnel:** skip CF GetConfiguration in applyRemoteConfig when snap matches (simplify G) ([08b846a](https://github.com/jacaudi/cloudflare-operator/commit/08b846aee6613f50645f0479f1abbcc16da25cdd))
* **zoneconfig:** fan out the 6 setting-group applies via errgroup ([b0078d7](https://github.com/jacaudi/cloudflare-operator/commit/b0078d786f41c4914889e4568552e7cb0c2770dc))


### BREAKING CHANGES

* CRDs now serve only cloudflare.io/v2alpha1. v1alpha1 is
removed; existing v1alpha1 objects require out-of-band migration.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>

## [0.18.3](https://github.com/jacaudi/cloudflare-operator/compare/v0.18.2...v0.18.3) (2026-05-09)

### Bug Fixes

* **tunnel:** self-heal apex CloudflareDNSRecord on out-of-band delete ([#109](https://github.com/jacaudi/cloudflare-operator/issues/109)) ([74c5a26](https://github.com/jacaudi/cloudflare-operator/commit/74c5a265f82591cc46ee047e01272e373da0d893))

## [0.18.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.18.1...v0.18.2) (2026-05-09)

### Bug Fixes

* **dns:** hard-fail on missing source labels for operator-emitted CRs ([#106](https://github.com/jacaudi/cloudflare-operator/issues/106)) ([e0cbf44](https://github.com/jacaudi/cloudflare-operator/commit/e0cbf449a710769a828b36046810831f4b79f211))
* **tunnel:** add source labels to apex CloudflareDNSRecord ([#106](https://github.com/jacaudi/cloudflare-operator/issues/106)) ([2752081](https://github.com/jacaudi/cloudflare-operator/commit/275208138a6df12fecb801ce72c9ea9f2dde71e4))

## [Unreleased]

### Fixed

- **`CloudflareTunnel` now self-heals when its apex `CloudflareDNSRecord` is deleted out from under it.** The tunnel controller's `SetupWithManager` was missing `.Owns(&CloudflareDNSRecord{})`, so deletion of an owned apex CR did not enqueue the parent tunnel. With `spec.apexHostname` still set, recovery required an out-of-band trigger (operator pod restart, tunnel spec change). With the watch wired up, deletion of the apex CR immediately fires a tunnel reconcile and `reconcileApexHostname`'s upsert path recreates the record on its own. Closes #109.
- **`CloudflareTunnel.spec.apexHostname` now works under registry-enabled config.** v0.17.0 / v0.18.0 / v0.18.1 emitted the apex `CloudflareDNSRecord` without `cloudflare.io/source-{kind,namespace,name}` labels, so when `RegistryConfig.TxtOwnerID` was set (the documented production config), `writeRegistryTXT` returned `ErrSourceLabelsMissing`, the companion TXT was never written, and the next reconcile saw a record without TXT and refused with `RegistryActionRefuseNoTXT` — the apex stayed in `Phase=Error, Reason=TxtRegistryGap` indefinitely and `ApexHostnameReady` never reached `True`. The apex CR now carries the four standard source labels matching the `HTTPRoute` / `Service` emitter convention. Additionally, the previously-silent skip on `ErrSourceLabelsMissing` is now a hard error when the CR carries `cloudflare.io/managed-by: cloudflare-operator`, so future operator-emitters that forget the labels surface immediately. **Migration:** if you have an apex CR stuck in `Phase=Error, Reason=TxtRegistryGap` after upgrade, delete it: `kubectl delete cloudflarednsrecord <tunnel-name>-apex -n <namespace>`. The DNSRecord controller's `reconcileDelete` will remove the orphaned Cloudflare record using the stored `Status.RecordID`; the next tunnel reconcile then rebuilds the apex CR from scratch with the new labels in place, and the DNSRecord controller creates a fresh Cloudflare record + companion TXT cleanly. (Setting `cloudflare.io/adopt: "true"` on the existing CR also works in principle, but the tunnel reconciler's periodic resync clobbers annotations via the upsert path, so the annotation can race away before the DNSRecord controller processes it — `kubectl delete` is deterministic.) A more automatic recovery path is tracked in #107. Closes #106.

## [0.18.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.18.0...v0.18.1) (2026-05-08)

### Bug Fixes

* **dns:** propagate spec.name changes to Cloudflare ([#104](https://github.com/jacaudi/cloudflare-operator/issues/104)) ([d85a184](https://github.com/jacaudi/cloudflare-operator/commit/d85a184cf30a00357e276d33967f5b26b4fcaa78))

## [0.18.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.17.0...v0.18.0) (2026-05-08)

### Bug Fixes

* **controller:** apex plumbing errors must not skip connector reconcile ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([f7d4722](https://github.com/jacaudi/cloudflare-operator/commit/f7d47228350ac7a751c1276a6507fc63462772d1))


### Features

* **api:** add CloudflareTunnel.spec.apexHostname types ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([d6aa674](https://github.com/jacaudi/cloudflare-operator/commit/d6aa67467d8a931cf667e1daeedcdd9ca4992b5c))
* **controller:** apex hostname helpers ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([cd16dfe](https://github.com/jacaudi/cloudflare-operator/commit/cd16dfe079f788d3a29cac0decd35556c618a165))
* **controller:** apex hostname reconciler orchestrator ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([ba0a054](https://github.com/jacaudi/cloudflare-operator/commit/ba0a054bb116ffca439f7ac77afd91e0032e1c6a))
* **controller:** resolveTunnelCNAME prefers apex when Ready ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([3a37f28](https://github.com/jacaudi/cloudflare-operator/commit/3a37f28d893b12a49bdf34d776af3895fce4075b))
* **controller:** wire apex reconciler into tunnel Reconcile ([#101](https://github.com/jacaudi/cloudflare-operator/issues/101)) ([8be1001](https://github.com/jacaudi/cloudflare-operator/commit/8be1001d33416759f0190d593b369b3cecdba070))

## [0.17.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.16.0...v0.17.0) (2026-05-08)

### Bug Fixes

* **api:** classify deletion-drain reasons as in-progress, not error ([c13d77a](https://github.com/jacaudi/cloudflare-operator/commit/c13d77a3b47e182ca328192d75d013d5ec2e8c58))
* **tunnel:** drain connector before DeleteTunnel ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([964d4c5](https://github.com/jacaudi/cloudflare-operator/commit/964d4c51ec42f48060fcf3164e0cfec5570f461b))


### Features

* **cfclient:** add IsTunnelHasActiveConnections predicate ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([fc37c2a](https://github.com/jacaudi/cloudflare-operator/commit/fc37c2a1dcc4c556371f11b877ab9ff3cd4db36c))
* **controller:** route 400/1022 to TunnelHasConnections ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([96d83ae](https://github.com/jacaudi/cloudflare-operator/commit/96d83ae97804c22c545bd99aed61210d8c7fee19))

## [0.16.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.15.0...v0.16.0) (2026-05-08)

### Bug Fixes

* **dns:** preserve Status.RecordID on transient GetRecord errors ([#85](https://github.com/jacaudi/cloudflare-operator/issues/85)) ([907aa43](https://github.com/jacaudi/cloudflare-operator/commit/907aa43f6b809436d7853dfa0a5960f7141e5e6b))
* **tunnel:** preserve Status.TunnelID on transient GetTunnel errors ([#85](https://github.com/jacaudi/cloudflare-operator/issues/85)) ([ef9d3f3](https://github.com/jacaudi/cloudflare-operator/commit/ef9d3f3f60b599ab32a703d1f2880fb89ea60903))


### Features

* **connector:** add cleanup helper for disabled connector ([#52](https://github.com/jacaudi/cloudflare-operator/issues/52)) ([05c6474](https://github.com/jacaudi/cloudflare-operator/commit/05c6474b7624164188459633d49e5cf3d2cf516c))
* **connector:** delete connector resources on disable ([#52](https://github.com/jacaudi/cloudflare-operator/issues/52)) ([ed11da3](https://github.com/jacaudi/cloudflare-operator/commit/ed11da3104c67d0ccb73812ea592f24f4799edc9))

## [0.15.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.14.1...v0.15.0) (2026-05-08)

### Bug Fixes

* **connector:** defer legacy cleanup until new connector is Ready ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([018cc1e](https://github.com/jacaudi/cloudflare-operator/commit/018cc1e595390647520a89f8faf82062c1541c99))
* **connector:** tighten cleanup unowned-test + soften godoc ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([8c54bdb](https://github.com/jacaudi/cloudflare-operator/commit/8c54bdba25d7c3074829a20cf32ad1ed2e40327e))


### Features

* **connector:** add legacy-name cleanup helper for default rename ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([c722989](https://github.com/jacaudi/cloudflare-operator/commit/c7229897c084502ea6cad686a380242a6f8ad4bf))
* **connector:** default base name to cloudflared-<tunnel> ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([53648c4](https://github.com/jacaudi/cloudflare-operator/commit/53648c427710ec63ff2a2efe2835c98c848efee0))
* **connector:** run legacy-name cleanup after successful apply ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([fbec355](https://github.com/jacaudi/cloudflare-operator/commit/fbec3557d9eccd5fd0f48cc121ec8530a0350d9a))

## [Unreleased]

### Added

- **`CloudflareTunnel.spec.apexHostname`**: opt-in operator-managed apex CNAME for a tunnel. When set, the operator reconciles a single `CloudflareDNSRecord` (named `<tunnel>-apex`) that CNAMEs to the tunnel's `.cfargotunnel.com` address; per-route records emitted by the HTTPRoute and Service source controllers CNAME to the apex instead of the tunnel UUID. Tunnel UUID rotation collapses to one record update — per-route records do not move. New `Status.ApexHostname` and `ApexHostnameReady` condition. Validation refuses to upsert when the apex name doesn't fall under the referenced zone or another `CloudflareDNSRecord` CR in the namespace already claims the same FQDN. Closes #101.

### Changed

- **CloudflareTunnel connector default rename.** The default base name for the operator-managed connector resources is now `cloudflared-<tunnel-name>` (was `<tunnel-name>-connector`). New resource names: Deployment / ServiceAccount `cloudflared-<tun>`, ConfigMap `cloudflared-<tun>-config`, PodDisruptionBudget `cloudflared-<tun>-pdb`. Pods now appear in `kubectl get pods` as `cloudflared-<tun>-...`, matching the Cloudflare upstream Helm chart. The connector reconciler automatically deletes the legacy `<tun>-connector` family of resources owned by your `CloudflareTunnel` on the next reconcile after upgrade, with no traffic gap (the new connector comes up first; both briefly coexist; legacy is then deleted). Users who set `spec.connector.nameOverride` are unaffected and the auto-cleanup is suppressed for them. Update any monitoring/alert filters that match the old pod-name prefix. (#93)
- **Active connector cleanup on `spec.connector.enabled: false`.** Previously, disabling the connector (or removing the `connector` block) left the Deployment, ServiceAccount, ConfigMap, and PodDisruptionBudget stranded until the parent `CloudflareTunnel` was deleted. The operator now deletes every operator-owned connector resource on the next reconcile after disable, discovered via the `app.kubernetes.io/name=cloudflared` + `cloudflare.io/tunnel=<name>` label set in the tunnel's namespace. Hand-applied resources matching the labels but lacking the owner-ref are left alone. As a side effect, resources from prior `spec.connector.nameOverride` values are also cleaned up by the same pass. Closes #52.

### Fixed

- **`CloudflareDNSRecord` rename now propagates to Cloudflare.** Editing `spec.name` on a CR with a populated `Status.RecordID` previously was a silent no-op — the controller's `needsUpdate` predicate did not compare `Name`, so the update branch never fired and the Cloudflare-side record stayed at the old name. The predicate now triggers on Name change and the existing `UpdateRecord` chain renames the record in place (Cloudflare's `PUT /zones/{zone_id}/dns_records/{id}` accepts `Name`, the SDK already passes it through). Removes the previously-documented "renaming the apex is not yet supported" caveat from `docs/tunnels.md` under `Primary apex hostname` (#101 follow-up). Closes #104.
- **Transient errors no longer wipe stored remote IDs.** The DNS and tunnel reconcilers previously cleared `Status.RecordID` / `Status.TunnelID` on any error from the in-flow Get-by-ID call, including transient 5xx responses or network blips. They now gate the ID-clear on `cfclient.IsNotFound(err)` (the same shape the zone reconciler uses); other errors propagate to the existing outer-`Reconcile` classifier, which preserves the stored ID and surfaces a `CloudflareAPIError` (or `PermissionDenied` / `BadRequest` / `PlanTierRequired`) condition `Reason` while the workqueue retries with backoff. Closes #85.
- **Connector deletion no longer deadlocks tunnel removal.** Deleting a `CloudflareTunnel` with `spec.connector.enabled: true` previously looped indefinitely with Cloudflare returning `400 code:1022` ("This tunnel has active connections"), because the operator's own `cloudflared` Deployment kept the tunnel busy and the operator never scaled it down. The deletion path now scales the managed Deployment to `replicas: 0` first, requeues until `Status.ReadyReplicas == 0`, then calls Cloudflare's DELETE. A transient `400 code:1022` returned even after local drain (the rare race where pods are gone but Cloudflare hasn't yet registered all connections closed) routes to a new `TunnelHasConnections` condition reason with a 30-second requeue. The status condition shows `DrainingConnector` while the drain is in progress. No manual `kubectl scale ... --replicas=0` workaround needed. Closes #100.

## [0.14.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.14.0...v0.14.1) (2026-05-06)

### Bug Fixes

* tunnel adoption secret collision and chart rollout strategy default ([#92](https://github.com/jacaudi/cloudflare-operator/issues/92)) ([c8de609](https://github.com/jacaudi/cloudflare-operator/commit/c8de6099262439433a53381fd269a4d57c223ca1)), closes [#90](https://github.com/jacaudi/cloudflare-operator/issues/90)

## [0.14.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.13.0...v0.14.0) (2026-05-05)

### Features

* add Status.Phase enum field to all six CRDs ([#89](https://github.com/jacaudi/cloudflare-operator/issues/89)) ([028cfd2](https://github.com/jacaudi/cloudflare-operator/commit/028cfd2a497bca2c0eadb381b445d4bca6b69820))

## [0.13.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.12.0...v0.13.0) (2026-05-04)

### Features

* label-gated Secret cache filter and SecretNotLabeled disambiguation ([#87](https://github.com/jacaudi/cloudflare-operator/issues/87)) ([22eb749](https://github.com/jacaudi/cloudflare-operator/commit/22eb7496c2b32045789584977b00488f3688d85c))


### BREAKING CHANGES

* User-supplied credential Secrets referenced via
secretRef must carry the new label cloudflare.io/managed=true or the
operator surfaces Ready=False with Reason=SecretNotLabeled. Migration:

  kubectl label secret -n <namespace> <name> cloudflare.io/managed=true

Operators with many Secrets to label can stage the migration by setting
the chart value secretCacheLabelSelector="" (or env
SECRET_CACHE_LABEL_SELECTOR="") to disable the filter, label their
Secrets, then restore the default.

In addition: on the delete-reconcile path, credential-load failures no
longer carry the wrapDeleteErr 'remove the finalizer manually to force
deletion' guidance in Condition.Message; the guidance is preserved in
operator logs. Reason classification on credential-load is now finer-
grained (adds ReasonSecretNotLabeled). New Warning recorder events fire
on credential-load failure during delete only when the failure is
ErrSecretNotLabeled.

* fix(connector): keep cloudflare.io/managed off Deployment+PDB selectors

CRITICAL upgrade hazard caught by independent comprehensive review:
adding cloudflare.io/managed=true to connectorLabels() inadvertently
flowed into Deployment.Spec.Selector and PodDisruptionBudgetSpec.Selector.
Both Spec.Selector fields are immutable — every reconcile on an existing
cluster upgrading from v0.12.0 would have failed with 'field is immutable.'

Fix: split into connectorSelectorLabels (4-key immutable subset) used at
the two Spec.Selector sites, and connectorLabels (5-key superset, includes
cloudflare.io/managed=true) used everywhere else (ObjectMeta.Labels, pod
template Labels, TSC LabelSelector). Pod-template labels carry the full
superset, so Selector matches via subset-of-pod-labels — works on both
old and new pods during a rolling restart.

Adds two regression tests pinning the 4-key selector subset; updates two
pre-existing PDB tests that asserted the 5-key set on Spec.Selector.

* docs(readme): troubleshoot stuck-deleting CRs with SecretNotLabeled

Reviewer feedback on the secret-cache-scoping arc — explicitly point
operators at the operator-log 'Remove the finalizer manually' guidance
when credential-load fails on the delete reconcile path. The Condition
message no longer carries the guidance (failReconcile path doesn't wrap
with wrapDeleteErr), only the operator log does.

## [0.12.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.11.0...v0.12.0) (2026-05-03)

### Features

* distinguish Cloudflare API failures via error classification predicates ([#86](https://github.com/jacaudi/cloudflare-operator/issues/86)) ([80519e3](https://github.com/jacaudi/cloudflare-operator/commit/80519e32460dc11502eb30e992e4a1bc9fb0327b)), closes [#4](https://github.com/jacaudi/cloudflare-operator/issues/4) [#6](https://github.com/jacaudi/cloudflare-operator/issues/6) [#7](https://github.com/jacaudi/cloudflare-operator/issues/7)

## [0.11.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.10.2...v0.11.0) (2026-05-01)

### Bug Fixes

* **connector:** add ownership guard + tighten PDB test assertions ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([7c5b931](https://github.com/jacaudi/cloudflare-operator/commit/7c5b93165c587b51ea66575afbc897fb69d14448))
* **connector:** address code review feedback for PDB build ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([7f91fb3](https://github.com/jacaudi/cloudflare-operator/commit/7f91fb3df038a93ef0ea1400e25416bd7712bdf7))
* **connector:** re-fetch existing PDB inside retry-on-conflict closure ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([f938499](https://github.com/jacaudi/cloudflare-operator/commit/f93849905205dfdff7a8ad10d8ac5942d236a486))


### Features

* **chart:** default PDB and topologySpreadConstraints at replicas>=2 ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([c8ccbfb](https://github.com/jacaudi/cloudflare-operator/commit/c8ccbfb85f7cd5b11ff3fb62d01006a049c95851))
* **connector:** add PodDisruptionBudget build function ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([d09e387](https://github.com/jacaudi/cloudflare-operator/commit/d09e3877455d531462cc82cc4c2094fee8f04d6c))
* **connector:** inject default per-hostname topologySpreadConstraint at replicas>=2 ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([8481698](https://github.com/jacaudi/cloudflare-operator/commit/8481698e408a5aba2fbedddc286cf2afd8bff53e))
* **connector:** reconcile connector PDB and watch owned PDBs ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([cd8d757](https://github.com/jacaudi/cloudflare-operator/commit/cd8d7578414097249c430a6ae1bb1cab7e087cab))

## [0.10.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.10.0...v0.10.2) (2026-05-01)

### Bug Fixes

* **tunnel:** defaultBackend inherits routing.originRequest ([#81](https://github.com/jacaudi/cloudflare-operator/issues/81)) ([#82](https://github.com/jacaudi/cloudflare-operator/issues/82)) ([7e4ed94](https://github.com/jacaudi/cloudflare-operator/commit/7e4ed94d60f1b20562cdef1d7fb9767ea9ba4f2d))

## [0.10.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.2...v0.10.0) (2026-05-01)

* feat(sources)!: rename emitted CR names to <kind>-<source-name>-<hash>[-txt] ([#71](https://github.com/jacaudi/cloudflare-operator/issues/71)) ([2d74b23](https://github.com/jacaudi/cloudflare-operator/commit/2d74b23c3146d2d9c43d49395fb21bcea6b023ff))


### BREAKING CHANGES

* emitted CR names change. Existing CRs are not migrated
and become orphans until owner-ref GC removes them or operators clean up
manually.

## [0.9.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.1...v0.9.2) (2026-05-01)

### Bug Fixes

* **connector:** httpGet readiness probe + safe rollout ([#75](https://github.com/jacaudi/cloudflare-operator/issues/75), [#76](https://github.com/jacaudi/cloudflare-operator/issues/76)) ([1dcbfcc](https://github.com/jacaudi/cloudflare-operator/commit/1dcbfcc2c6f716eb4bb5efb7b1f63928b6350b29))

## [0.9.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.0...v0.9.1) (2026-05-01)

### Bug Fixes

* **sources:** propagate secret namespace into emitted SecretRef ([#70](https://github.com/jacaudi/cloudflare-operator/issues/70)) ([17b1909](https://github.com/jacaudi/cloudflare-operator/commit/17b19095d1d84b144625890d920cd66f4efb3e1d))

## [0.9.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.8.1...v0.9.0) (2026-05-01)

### Features

* **connector:** add spec.connector.nameOverride to customize resource names ([#68](https://github.com/jacaudi/cloudflare-operator/issues/68)) ([0f87275](https://github.com/jacaudi/cloudflare-operator/commit/0f87275f6867e30c493307a8912db6b98548f4ac))

## [0.8.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.8.0...v0.8.1) (2026-05-01)

### Bug Fixes

* **connector:** drop redundant http_status:404 when defaultBackend is set ([#66](https://github.com/jacaudi/cloudflare-operator/issues/66)) ([2a10741](https://github.com/jacaudi/cloudflare-operator/commit/2a107417e3fe174f5d0bd3bc119c20789855ae72))
* **sources:** propagate zone namespace into emitted ZoneRef ([#65](https://github.com/jacaudi/cloudflare-operator/issues/65)) ([ed78538](https://github.com/jacaudi/cloudflare-operator/commit/ed7853878910fc13c185ba36d2d242746498435b))

## [0.8.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.4...v0.8.0) (2026-05-01)

### Bug Fixes

* **connector:** drop redundant --credentials-file Args ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58)) ([a8d5833](https://github.com/jacaudi/cloudflare-operator/commit/a8d58336da88b49bf12a0c9b9f8ff005e1f66de9))
* **connector:** render tunnel and credentials-file in config.yaml ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58)) ([e1055ef](https://github.com/jacaudi/cloudflare-operator/commit/e1055efa53ff980124cf6254900b166381e42a00))
* **connector:** retry on conflict in applyOwned (SA, ConfigMap) ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59)) ([655e640](https://github.com/jacaudi/cloudflare-operator/commit/655e64097f0e7e22e6dcd06f1ecacbcef44efbcd))
* **connector:** retry on conflict when updating connector Deployment ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59)) ([f6a1d72](https://github.com/jacaudi/cloudflare-operator/commit/f6a1d729622271db326f2c904d296098d87f2f58))


### Features

* **connector:** fail loud when Status.TunnelID is empty ([3ad01b2](https://github.com/jacaudi/cloudflare-operator/commit/3ad01b25c3907d9979d8a67130999040d8a9d97d))

## [0.7.4](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.3...v0.7.4) (2026-05-01)

### Bug Fixes

* **deps:** update module sigs.k8s.io/gateway-api to v1.5.1 ([4fd3e67](https://github.com/jacaudi/cloudflare-operator/commit/4fd3e670ff99dcf481d7c933f70d207a78f0400b))

## [0.7.3](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.2...v0.7.3) (2026-05-01)

### Bug Fixes

* **deps:** update kubernetes-client-libraries ([7f064c2](https://github.com/jacaudi/cloudflare-operator/commit/7f064c235541cdd373dca5fca2e01fef94d900c5))

## [0.7.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.1...v0.7.2) (2026-05-01)

### Bug Fixes

* **deps:** update module github.com/cloudflare/cloudflare-go/v6 to v6.10.0 ([ee06089](https://github.com/jacaudi/cloudflare-operator/commit/ee06089394aa6b0383609c8059b68f8162e455ae))
* **deps:** update test-libraries ([722cd83](https://github.com/jacaudi/cloudflare-operator/commit/722cd832afa8e11913e9ac08ec5cee00e66f2230))

## [0.7.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.0...v0.7.1) (2026-05-01)

### Bug Fixes

* **deps:** update module golang.org/x/sync to v0.20.0 ([7270f2c](https://github.com/jacaudi/cloudflare-operator/commit/7270f2c14b04e8fca1b1e54fedf4ba2c7b6af0e5))

## [0.7.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.2...v0.7.0) (2026-04-30)

### Features

* **crd:** close TF-parity gaps on CloudflareZoneConfig + Ruleset ([#57](https://github.com/jacaudi/cloudflare-operator/issues/57)) ([#61](https://github.com/jacaudi/cloudflare-operator/issues/61)) ([90f7e02](https://github.com/jacaudi/cloudflare-operator/commit/90f7e02e5f5632f4a88ab60c763698e50f261bfb))

## [0.6.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.1...v0.6.2) (2026-04-28)

### Bug Fixes

* **zoneconfig:** populate status.zoneID and use it for printcolumn ([d4c4c9a](https://github.com/jacaudi/cloudflare-operator/commit/d4c4c9ab9799990f6c581864a56af06a4cc8d60d))

## [0.6.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.0...v0.6.1) (2026-04-27)

### Bug Fixes

* **zoneconfig:** apply settings groups independently (closes [#51](https://github.com/jacaudi/cloudflare-operator/issues/51)) ([#56](https://github.com/jacaudi/cloudflare-operator/issues/56)) ([f846ae3](https://github.com/jacaudi/cloudflare-operator/commit/f846ae3a618ec2218461d9a7dd4b5ed7d7ed1c7e))

## Unreleased

### Added

- `CloudflareTunnel.spec.connector.nameOverride`: optional field to set the base name used for the operator-managed Deployment, ServiceAccount, and ConfigMap. When set, the Deployment and ServiceAccount are named exactly `<nameOverride>` and the ConfigMap is named `<nameOverride>-config`. When unset, the existing `<tunnel.metadata.name>-connector` family is preserved. ([#68](https://github.com/jacaudi/cloudflare-operator/issues/68))

### Fixed

- `CloudflareZoneConfig`: a permission/plan failure on one settings group (most commonly `bot_management` on Free zones, or a token without `Zone:Bot Management:Edit`) no longer blocks the rest of the spec from being applied. Each group now records its own `<Group>Applied` status condition with reason `Applied`, `NotConfigured`, `PermissionDenied`, or `CloudflareAPIError`. The resource's `Ready` condition is `False` with `Reason=PartialApply` until every configured group succeeds. ([#51](https://github.com/jacaudi/cloudflare-operator/issues/51))
- `CloudflareTunnel.spec.connector`: the operator-managed `cloudflared` Deployment now starts cleanly. Previously the rendered Args ended at `tunnel ... run` with no positional tunnel UUID and the rendered `config.yaml` contained only an `ingress:` block, so cloudflared exited with `"cloudflared tunnel run" requires the ID or name of the tunnel`. The aggregator now writes top-level `tunnel:` and `credentials-file:` keys into the rendered config so cloudflared can resolve the tunnel from the config alone; the Deployment Args drop `--credentials-file` accordingly so identity has a single source of truth. ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58))
- `CloudflareTunnel`: deletion no longer wedges when the connector reconcile loop is hitting sustained optimistic-concurrency conflicts. The connector Deployment, ConfigMap, and ServiceAccount apply paths now retry conflicts in-process via `retry.RetryOnConflict`, so transient ResourceVersion churn does not propagate as reconcile errors that inflate the controller workqueue's exponential backoff. Finalizer cleanup runs promptly without an operator pod restart. ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59))
- HTTPRoute and Service source controllers now propagate the `CloudflareZone` CR's namespace into the emitted `CloudflareDNSRecord.spec.zoneRef.namespace`. Previously, only `zoneRef.name` was set; the downstream `ResolveZoneID` helper defaulted the lookup to the dependent CR's own namespace, so reconciliation failed with `CloudflareZone … not found` whenever the zone CR lived in a different namespace from the source object. The `ZoneReference` API type gains an optional `namespace` field for this. ([#65](https://github.com/jacaudi/cloudflare-operator/issues/65))
- `CloudflareTunnel.spec.routing.defaultBackend`: cloudflared no longer crash-loops when a default backend is configured and zero `CloudflareTunnelRule` entries are included in aggregation. The aggregator previously emitted both the user's `defaultBackend` (a no-`hostname:` catch-all) AND the auto-appended `http_status:404`, which cloudflared correctly rejects as two consecutive catch-alls (`Rule #1 is matching the hostname '', but this will match every hostname`). The trailing `http_status:404` is now emitted only when no `defaultBackend` is set. ([#66](https://github.com/jacaudi/cloudflare-operator/issues/66))

## [0.6.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.5.1...v0.6.0) (2026-04-26)

### Features

* v1 Gateway API sources + tunnel runtime (closes [#44](https://github.com/jacaudi/cloudflare-operator/issues/44) [#47](https://github.com/jacaudi/cloudflare-operator/issues/47) [#48](https://github.com/jacaudi/cloudflare-operator/issues/48) [#49](https://github.com/jacaudi/cloudflare-operator/issues/49)) ([#53](https://github.com/jacaudi/cloudflare-operator/issues/53)) ([106f621](https://github.com/jacaudi/cloudflare-operator/commit/106f62156a208dfc7e8d6c516ea14ed90d4b002a)), closes [#46](https://github.com/jacaudi/cloudflare-operator/issues/46) [#52](https://github.com/jacaudi/cloudflare-operator/issues/52)

### Added
- New CRD `CloudflareTunnelRule` — primitive for cloudflared ingress rules, emitted by source controllers or hand-authored.
- `CloudflareTunnel.spec.connector` — operator-managed cloudflared Deployment + ConfigMap + ServiceAccount.
- `CloudflareTunnel.spec.routing.defaultBackend` — tunnel-wide default backend routing.
- New source controllers:
  - `httproute_source` — watches Gateway API `HTTPRoute` + `Gateway`, emits DNS from `cloudflare.io/*` annotations.
  - `service_source` — watches `Service`, emits DNS + TunnelRule from `cloudflare.io/*` annotations.
- External-dns-compatible plaintext TXT ownership registry in `internal/cloudflare/txt_registry.go` for record adoption and conflict detection during migration.
- New operator config: `TXT_OWNER_ID` (required to activate sources), `TXT_IMPORT_OWNERS`, `TXT_PREFIX`, `TXT_SUFFIX`, `TXT_WILDCARD_REPLACEMENT`.

### Changed
- RBAC cluster role now includes cluster-wide write on `deployments`/`configmaps`/`serviceaccounts` (needed because tunnel CRs can live in any namespace). Scoped in practice by ownerRef to `CloudflareTunnel`.
- Operator image requires `sigs.k8s.io/gateway-api` CRDs installed in the cluster.

### Migration
- v0.5.x → v0.6.0 is a pure add. Without `TXT_OWNER_ID` set, annotation-driven sources are inert and existing behavior is unchanged.
- To activate: install Gateway API CRDs → set `TXT_OWNER_ID` → annotate workloads. See [docs/external-dns-migration.md](docs/external-dns-migration.md) for drop-in paths from external-dns.

## [0.5.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.5.0...v0.5.1) (2026-04-21)

### Bug Fixes

* **controller:** log zone-ref-not-ready at Info, not Error ([#41](https://github.com/jacaudi/cloudflare-operator/issues/41)) ([8e3e049](https://github.com/jacaudi/cloudflare-operator/commit/8e3e049ec2424fa02a8e142d5b0d12200d8a0410))
* **ruleset:** use phase entrypoint API instead of POSTing new custom ruleset ([#45](https://github.com/jacaudi/cloudflare-operator/issues/45)) ([68d7106](https://github.com/jacaudi/cloudflare-operator/commit/68d71068a1bd5cf0f7c132dfde4bcf333291a352)), closes [#43](https://github.com/jacaudi/cloudflare-operator/issues/43)

## [0.5.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.4.0...v0.5.0) (2026-04-21)

* feat!: account ID to secret + pipeline alignment with nextdns-operator ([#38](https://github.com/jacaudi/cloudflare-operator/issues/38)) ([32989f9](https://github.com/jacaudi/cloudflare-operator/commit/32989f98be742ecd928c633c2e39e105682104e6))


### BREAKING CHANGES

* spec.accountID has been removed from CloudflareZone
and CloudflareTunnel. Add an `accountID` key to the API token Secret
and remove the field from existing CRs before upgrading.

## [0.4.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.3.1...v0.4.0) (2026-04-19)

### Features

* **zoneconfig:** spec-hash drift detection, drop AppliedSettings ([da2a550](https://github.com/jacaudi/cloudflare-operator/commit/da2a55003a7e0d1d61c5cca2f4aa6240f6c2e3a5))

## [0.3.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.3.0...v0.3.1) (2026-04-18)

### Bug Fixes

* **ci:** use v-prefixed tag for container and helm chart releases ([d3e6beb](https://github.com/jacaudi/cloudflare-operator/commit/d3e6beb31f5da18552958a9cd0c5e22e35d96de8))

## [0.3.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.2.0...v0.3.0) (2026-04-18)

* feat(logging)!: replace zap with log/slog in main ([d34966f](https://github.com/jacaudi/cloudflare-operator/commit/d34966f2bbb2bf0035f6e11c143e755f73634ad9)), closes [#19](https://github.com/jacaudi/cloudflare-operator/issues/19)


### Bug Fixes

* **deps:** update kubernetes-client-libraries ([2426281](https://github.com/jacaudi/cloudflare-operator/commit/2426281e9117602f9bbcde8456d7c73bedd0b298))
* **deps:** update test-libraries ([64c8ca3](https://github.com/jacaudi/cloudflare-operator/commit/64c8ca3080a4d051f9998fc502f78d874f327aec))


### Features

* **logging:** add slog setupLogger helper with tests ([0428618](https://github.com/jacaudi/cloudflare-operator/commit/042861899a11018adb5691adf9cc7142925d5ae2)), closes [#19](https://github.com/jacaudi/cloudflare-operator/issues/19)


### BREAKING CHANGES

* --zap-devel, --zap-encoder, --zap-log-level,
--zap-stacktrace-level, --zap-time-encoding are no longer accepted.
Use --log-level (debug|info|warn|error) and --log-format (json|text)
instead.
