# Turnkey bootstrap and verification

Status: implementation slice for issue #138; stable acceptance remains pending
Last updated: 2026-07-28

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

## Clean-install sequence

The first two commands are read-only. Keep the generated plan as the reviewed
change record.

```text
waycloakctl preflight --context <context> --overlay-cidr <reviewed-cidr> --output human
waycloakctl install plan --context <context> --release-manifest <verified-release.json> >install-plan.json
waycloakctl install apply --context <context> --plan install-plan.json --confirm <planID>
```

The release manifest must satisfy
[`release-manifest-v1.schema.json`](../api/release-manifest-v1.schema.json) and
name the exact chart, controller, CNI, node-agent, gateway-agent, Gluetun, and
pause digests. `install plan` repeats preflight and refuses an incompatible
cluster. The plan lists namespace privilege, host CNI paths, exact Helm values,
Secret object names, rollback, and purge boundaries. It never contains Secret
data. `install apply` accepts only the exact recalculated plan ID, creates the
observation CA and serving key in memory, stores them directly as Kubernetes
Secrets, and never passes private key material through Helm.

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
with `gateway init --allow-disruptive-verify`, an immutable curl probe image, and
the exact confirmation digest printed by a refused unconfirmed invocation. It
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
TCP/UDP forwarding, and tunnel-loss denial. Read-only homelab preflight correctly
refuses the currently served alpha API without mutating the cluster.

Issue #138 must remain open until a published signed CLI artifact completes the
supported clean-cluster Proton/OpenVPN journey in under 15 minutes and the
confirmation-gated `verify` scenario passes with exact release artifacts. Kind
installation coverage and the release signature/SBOM/provenance run are also
required evidence; implementation alone is not certification.
