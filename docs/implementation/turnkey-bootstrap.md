# Turnkey bootstrap and verification

Status: implementation slice for issue #138; stable acceptance remains pending
Last updated: 2026-08-09

`waycloakctl` is a stateless assistant around the Helm and Kubernetes APIs. It
does not become a controller, store VPN credentials, translate alpha objects,
or weaken an unsupported cluster into a nominally successful install. JSON
outputs use `cli.waycloak.io/v1`.

## Artifact verification

Download one immutable `waycloakctl-<os>-<arch>` binary together with
`SHA256SUMS`, `SHA256SUMS.sigstore.json`, and `waycloakctl.spdx.json` from the
same release. Verify the checksum and keyless Sigstore bundle before running
the binary. The release workflow builds each supported binary twice, compares
the outputs, publishes an SPDX SBOM, and records GitHub build provenance.
Tags containing a SemVer prerelease suffix are published as GitHub
prereleases. A separate hosted-runner job downloads the published asset set and
rejects a missing or extra asset, checksum mismatch, unexpected Sigstore
workflow identity or issuer, non-tag source reference, wrong source commit, or
self-hosted provenance before the release run can pass.

An operator can independently repeat the important checks after downloading
one release into an empty directory:

```text
sha256sum --check SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
cosign verify-blob --bundle waycloakctl.spdx.sigstore.json \
  --certificate-identity https://github.com/Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml@refs/tags/<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com waycloakctl.spdx.json
gh attestation verify waycloakctl-<os>-<arch> --repo Amoenus/waycloak \
  --signer-workflow Amoenus/waycloak/.github/workflows/waycloakctl-release.yaml \
  --source-ref refs/tags/<tag> --deny-self-hosted-runners
```

## Clean-install sequence

The first two commands are read-only. Keep the generated plan as the reviewed
change record.

```text
waycloakctl preflight --context <context> --overlay-cidr <reviewed-cidr> --output human
waycloakctl install plan --context <context> --release-manifest <verified-release.json> \
  --node-architecture <amd64-or-arm64> >install-plan.json
waycloakctl install apply --context <context> --plan install-plan.json --confirm <planID>
```

The architecture flag is optional only when preflight observes exactly one
architecture. A mixed-architecture cluster requires an explicit reviewed row.
The generated values constrain both the CNI installer and node agent to that
architecture; only those nodes can publish the exact `core-ready` capability
that enrolled workloads select. Building a multi-platform image is not treated
as conformance evidence for an otherwise unproved node row.

The release manifest must satisfy
[`release-manifest-v1.schema.json`](../api/release-manifest-v1.schema.json) and
name the exact chart, controller, CNI, node-agent, gateway-agent, Gluetun, and
pause digests. Its `manifestDigest` is verified against canonical JSON over
every identity field except the digest itself; profile order and insignificant
file formatting do not change that identity, while any artifact or version
change does. Extra image entries are rejected. `install plan` repeats preflight
and refuses an incompatible cluster. The plan lists namespace privilege, host
CNI paths, exact Helm values,
Secret object names, rollback, and purge boundaries. It never contains Secret
data. Preflight hashes the trusted server/CA/cluster identity, Kubernetes and
exact runtime versions, architectures, kernels, operating systems, primary-CNI
identity, network observations, and check results. The install plan binds that
observation digest, overlay, and selected architecture into its identity.
`install apply` accepts only the exact recalculated plan ID, re-runs the same
preflight before any Namespace or Secret creation, and refuses changed or
incompatible observations. It then creates the
observation CA and serving key in memory, stores them directly as Kubernetes
Secrets, and never passes private key material through Helm.

On a clean cluster, the reviewed apply is deliberately staged. The first Helm
revision installs the CRDs and starts the controller while the CNI installer,
node agent, and default class remain disabled. After that revision is Ready, a
second revision activates the exact reviewed Core runtime. This prevents the
new chained CNI from becoming authoritative before the ordinary-networked
controller exists. New Pod sandboxes may fail closed during Core activation;
there is no namespace bypass or fail-open interval. An already-deployed Helm
release goes directly to the full reviewed revision so re-apply and upgrade do
not temporarily remove its deny path.

The node agent resolves each CNI request's exact Pod UID and node assignment
with a direct API-server read. Its informer cache remains useful for ordinary
reconciliation, but it is not authoritative for creation-time identity or Pod
name reuse. The direct reader uses a distinct projected token with the API
server's default audience; the audience-bound observation token is isolated and
cannot be used as a portable Kubernetes API credential.

Release automation, never the cluster operator, assembles this input with the
publisher-only `go run ./hack/corerelease` command. The command requires an
exact OCI chart identity and exactly the replacement controller, CNI, node
agent, gateway agent, Gluetun, and pause image identities. It performs no tag
resolution or registry discovery and rejects missing, extra, duplicate, or
mutable inputs before emitting deterministic JSON. The resulting manifest is
then signed and published by the release lifecycle; installation consumes that
verified file without requiring source or image-digest knowledge.

CI also constructs a disposable Kind cluster with a job-local OCI registry and
uses the real CLI boundary for preflight, plan, and apply. The acceptance first
proves that an incorrect confirmation creates no namespace, then verifies the
exact chart and runtime image identities, release-bound CNI receipt and chain,
authenticated node capability, default class identity, and healthy doctor
output. It then builds exact disposable fixture artifacts, creates runtime-only
WireGuard keys and TLS, and runs the confirmation-gated disruptive verification
against one HTTPS observer reached through ordinary and protected paths. The
gate proves refusal without mutation, distinct observed source addresses,
exact-UID gateway replacement, protected application startup denial during the
loss window, ordinary-network continuity, recovery, and exact cleanup. The
fixture is CI evidence for the Core mechanics, not a supported VPN provider and
not a substitute for the signed published-artifact or real-provider gates.

Normal Helm uninstall intentionally does not restore the primary CNI chain or
delete CRDs. Those are separate destructive operations covered by issue #139.
An alpha API causes `preflight` to fail: stop protected workloads and complete
the explicit alpha purge procedure before installing the replacement. There is
no migration or conversion path.

For an existing alpha installation, use the separately reviewed
[destructive purge runbook](../operations/alpha-purge-and-reinstall.md).
`waycloakctl alpha-purge plan` is read-only; `alpha-purge apply` binds deletion
to exact cluster/CR/CRD UID fingerprints and requires explicit runtime-empty and
separate-uninstall attestations.

## Gateway and workload verification

Create the provider credential Secret separately, then render non-secret native
configuration and a `VPNGateway` that only references it:

```text
waycloakctl gateway init --namespace <namespace> --name <gateway> \
  --config-map <config-map> --secret <credential-secret> \
  --overlay-cidr <reviewed-cidr> >gateway.yaml
kubectl apply -f gateway.yaml
waycloakctl doctor --namespace <namespace> --output human
```

The initial reviewed recipe is Proton/OpenVPN through Gluetun. The application
Pod receives only the `networking.waycloak.io/egress-route` label; it receives
no Waycloak sidecar, init container, capability, host mount, VPN credential, or
Kubernetes credential.

`waycloakctl verify` is deliberately disruptive. It requires a gateway rendered
with `gateway init --allow-disruptive-verify`, an immutable Waycloak probe image,
and the exact confirmation digest printed by a refused unconfirmed invocation.
The probe receives an HTTPS observer URL and, when needed, a same-namespace
ConfigMap containing only public `ca.crt`; both names are bound into the
confirmation digest. It receives no service-account token or Secret. The command
creates only run-labeled route/probe objects, proves ordinary and protected
egress differ, deletes the exact UID-owned gateway Pod, proves new protected
application containers do not start during loss while ordinary networking
continues, waits for recovery, and deletes only its own objects.

## Diagnostics and support bundle

`waycloakctl doctor` reports resource identity, generation, allowlisted
conditions, and authenticated node capability counts. It omits condition
messages, addresses, endpoints, object specs, Secret data, and ConfigMap data.

```text
waycloakctl support-bundle --context <context> --file waycloak-support-bundle.tar.gz
```

The deterministic, mode `0600` bundle contains preflight, doctor, and reduced
event summaries plus per-section digests. It excludes raw event messages,
logs, addresses, endpoints, credentials, keys, Secret objects/data, and
ConfigMap data. Review it before sharing.

## Current evidence and remaining acceptance

Unit tests cover unsupported clusters, alpha refusal, overlapping networks,
plan tampering, exact confirmation, certificate isolation, credential canary
redaction, deterministic bundles, and exact UID-scoped gateway disruption. A
privileged network-namespace test proves the gateway deny-first path, healthy
TCP/UDP forwarding, and tunnel-loss denial. The mandatory chained-CNI installer
is built twice as a Linux amd64/arm64 OCI layout and compared byte-for-byte in
CI. All four source-owned Core images also share one composite OCI build target,
and deterministic manifest assembly is exercised as a command-line boundary in
CI. Read-only homelab preflight correctly refuses the currently served alpha API
without mutating the cluster.

The `v0.0.0-turnkey.1` prerelease executed the signed CLI workflow from exact
main commit `21ffebea3444f830ec2c9b29acebd9b36a2fd878`. Release run
`30360505871` passed publication and the separate hosted-runner verification of
the complete downloaded asset set.

Core deployment candidates use `vMAJOR.MINOR.PATCH-core.NUMBER` tags. The Core
workflow repeat-builds the four Waycloak amd64/arm64 binaries, packages the
chart with the tag-derived immutable version, publishes only digest-resolved
identities, and assembles `core-release-manifest.json` with the derived Gluetun
and pinned pause identities. The Gluetun image is built from upstream commit
`7eed6eaf160440724a93ca66f66055068cebe4ac` on upstream multi-platform image
digest `sha256:e3272b29a4bc177b389fbdcb54cf9716ccbfc30f04d8b7a35b0a5be9cdb58461`.
Only the reachable fixed Go dependencies are advanced; the release includes
the exact patch, binary checksums, and preserved upstream MIT license.
Call-graph analysis, upstream privileged tests, and the image vulnerability
scan must pass. Publication is refused on HIGH/CRITICAL fixed vulnerabilities.
Every published OCI artifact receives an SPDX attestation, keyless signature,
and GitHub provenance; release files receive signed checksums and provenance. A
separate hosted runner redownloads the release and
verifies the exact workflow identity, issuer, source tag/commit, platform
indexes, chart bytes/CRDs, manifest-to-registry equality, and all signatures and
attestations. The workflow is not evidence until an exact tag run passes.

Issue #138 must remain open until the published Core candidate completes the
supported clean-cluster Proton/OpenVPN journey in under 15 minutes. The
exact-artifact Kind installation and disruptive fixture coverage do not replace
that provider proof.
