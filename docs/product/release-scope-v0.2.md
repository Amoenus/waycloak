# v0.2.0 OCI adoption release scope

Status: Accepted release scope
Owner: Waycloak maintainers
Last updated: 2026-07-14

## Outcome

`v0.2.0` is the first productized port-forward release and the release that
replaces the originating homelab PoC. It freezes the implemented single-gateway
private-egress and provider-neutral port-forward feature set into one signed,
immutable OCI bundle.

The release candidate is deployed as the real homelab implementation before
the final tag. Findings that prevent ordinary protected egress, DNS, provider
port delivery, qBitTorrent operation, or fail-closed behavior block the final
release. Additional certification scenarios do not silently expand this
release; they are versioned follow-up work.

## Included product surface

- `VPNGateway`, controller-owned `VPNWorkload`, and `PortForwardLease`
  `v1alpha1` APIs;
- annotation-based, UID-bound, fail-closed workload injection;
- stable overlay allocations and stable provider lease identities;
- the Gluetun Proton/OpenVPN gateway and native VXLAN/nftables data plane;
- gateway-contained DNS and observed readiness;
- Proton NAT-PMP renewal, atomic TCP/UDP DNAT, neutral renewable delivery, and
  provider-assigned application-port handoff;
- the separate least-privilege qBitTorrent adapter;
- Helm as the primary installer and KCL as an optional authoring adapter.

## Complete OCI bundle

One release manifest records immutable digests for:

1. the controller/webhook image;
2. the injected agent image;
3. the gateway-manager image;
4. the qBitTorrent adapter image;
5. the Helm OCI chart, including every served CRD under `crds/`;
6. the optional KCL OCI module generated from those same CRDs.

Every registry artifact is signed. Images and packages carry appropriate SBOM
and build-provenance evidence. Human-friendly semantic-version tags are lookup
aliases; installations resolve and retain digest identities from the signed
manifest.

The KCL module renders the same Kubernetes API and annotations as plain YAML.
It contains no provider credentials, private endpoints, homelab values, or
runtime controller logic. Waycloak has no runtime dependency on KCL,
Crossplane, Argo CD, or the originating composition stack.

## Adoption acceptance

The `v0.2.0-alpha.9` candidate is installed from its verified OCI identities
and replaces the legacy pod-gateway/qSticky route for the originating
qBitTorrent workload. Acceptance requires observed normal operation:

- the protected Pod is admitted only through Waycloak and starts after its
  UID-bound allocation exists;
- external egress and DNS traverse the selected gateway;
- the provider port is delivered to qBitTorrent through the released adapter;
- tracker and DHT operation are healthy during ordinary use;
- deleting or stopping the gateway does not restore direct egress;
- active Pods contain neither the legacy pod-gateway/qSticky components nor
  VPN credentials or Kubernetes credentials introduced by Waycloak;
- rollback to the previous deployment remains documented until acceptance is
  complete.

The consumer repository owns its ExternalSecret, namespace labels, workload
configuration, private hostnames, and rollout. None of those values become
Waycloak product defaults or release evidence.

## Explicitly deferred to v0.3.0

- a forced sustained provider crawl proving a particular number of renewals;
- waiting for an actual Proton-assigned public-port change on demand;
- formal qBitTorrent DHT certification across that forced rotation;
- Bitmagnet and Loadstone consumption and any evidence-backed adapters;
- additional provider/application compatibility certification.

These remain valuable tests and product work. They do not change the API or
invalidate the `v0.2.0` functionality already shipped; GitHub milestone
`v0.3.0` owns their completion.

## Explicitly deferred to v0.4.0

- multi-gateway sharding and failover;
- backup, restore, and disaster-recovery certification;
- optional metrics, alerts, and dashboards;
- performance benchmarks and the broader Kubernetes/CNI compatibility matrix.

## Release sequence

1. Merge scope, KCL module, manifest, and workflow support into `main`.
2. Publish and independently verify the complete signed alpha.6 OCI bundle.
   Earlier attempts stopped before release when the new verifier found a KCL
   library-consumption assumption, a spec-compliant Helm media-type omission,
   and the immutable KCL package version boundary. Alpha.6 includes those
   fixes plus rotation-safe optional cert-manager webhook TLS. The first staged
   homelab deployment then exposed a zero-member gateway bootstrap cycle before
   any workload was migrated; alpha.7 fixed that release-blocking finding while
   keeping VXLAN ingress deny-first until members are observed. The live alpha.7
   upgrade then exposed an obsolete controller condition that still described
   port forwarding as unimplemented; alpha.8 connects gateway readiness to the
   implemented manager capability and reconciliation observation.
   The first generated workload Pod then exposed that Kubernetes assigns a
   Deployment Pod's final generated name after mutating admission. Alpha.9
   derives the allocation marker from the unique admission request identity,
   persists it on the Pod, and makes both validation and reconciliation consume
   that marker without weakening the UID-bound ConfigMap ownership check.
3. Replace the homelab PoC with the exact alpha.9 candidate.
4. Fix only release-blocking adoption findings through reviewed main-branch
   changes and a new candidate when required.
5. Publish final signed `v0.2.0`, update status and adoption evidence, and close
   the milestone.
