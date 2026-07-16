# Author and install a workload adapter

A workload adapter is appropriate only when an application must learn a
provider-assigned port through a proprietary local API. Applications that can
listen on the stable target port need no adapter.

The public wire contract and portable conformance vectors live in
[`protocol/adapter/v1alpha1`](../../protocol/adapter/v1alpha1/README.md). An
adapter can be written in any language and must not import Waycloak Go
packages.

## Minimal implementation

An adapter loop performs four operations:

1. GET the URL in `WAYCLOAK_LEASE_ENDPOINT` and select exactly one current
   `ProviderAssigned` record.
2. Apply its `applicationPort` through the application's loopback API.
3. Verify the application is actually listening on that port.
4. POST this acknowledgement to
   `$WAYCLOAK_LEASE_ENDPOINT/{identity}/ack`:

```json
{
  "apiVersion": "networking.waycloak.io/adapter/v1alpha1",
  "podUID": "<document.podUID>",
  "leaseIdentity": "<record.identity>",
  "generation": 7,
  "applicationPort": 42000
}
```

The adapter starts unready and exposes a readiness probe that succeeds only
for the exact current applied revision. It clears readiness on rotation,
expiry, missing or duplicate records, Pod UID change, acknowledgement
rejection, and sustained application API failure. Retry with capped
exponential backoff and jitter; never log credentials or response bodies.

The [independent Python sample](../../examples/workload-adapter-python/README.md)
implements this loop using only its language standard library and the
published JSON/HTTP fields.

## Package the image

Build a static or otherwise minimal image for `linux/amd64` and `linux/arm64`.
Run it as non-root with a read-only root filesystem, RuntimeDefault seccomp,
all capabilities dropped, no privilege escalation, and no host namespaces or
ports. Add the required `io.waycloak.adapter.*` OCI labels, publish by digest,
scan it, attach an SPDX SBOM and provenance, and sign the index.

Before installation, the operator verifies the digest and evidence, then
creates the cluster-scoped trust record:

```yaml
apiVersion: networking.waycloak.io/v1alpha1
kind: WorkloadAdapter
metadata:
  name: example
spec:
  protocolVersion: networking.waycloak.io/adapter/v1alpha1
  image: ghcr.io/example/adapter@sha256:<64 lowercase hexadecimal characters>
```

Creating this object is the operator trust decision. A workload author cannot
make Waycloak trust an arbitrary image through an annotation.

## Select the adapter

The workload explicitly includes the adapter container and selects both the
trusted object and container on its Pod template:

```yaml
metadata:
  annotations:
    networking.waycloak.io/gateway: waycloak-system/private-egress
    networking.waycloak.io/workload-adapter: example
    networking.waycloak.io/adapter-container: waycloak-adapter
spec:
  automountServiceAccountToken: false
  containers:
    - name: waycloak-adapter
      image: ghcr.io/example/adapter@sha256:<same digest>
      readinessProbe:
        exec:
          command: [/adapter, probe]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
        readOnlyRootFilesystem: true
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
```

Admission requires the image to exactly match the `WorkloadAdapter`, validates
the security posture, and adds only the protocol and loopback endpoint
environment variables. Application-owned Secrets or ConfigMaps are mounted by
the workload author only into the selected adapter. Waycloak does not copy
them, and the adapter receives no Kubernetes token or VPN credentials.

## Security and release checklist

- Exact digest exists in an operator-created `WorkloadAdapter`.
- Signature identity, provenance, SBOM, vulnerability policy, OCI labels, and
  both supported architectures are verified before trust is granted.
- The adapter uses only loopback application and Waycloak endpoints.
- No Kubernetes API client, service-account token, VPN Secret, hostPath,
  hostPort, device, capability, or privileged security context is present.
- Credentials are workload-owned, narrowly mounted, redacted from errors, and
  never included in readiness responses.
- Every published conformance vector passes, including rotation and negative
  identity/generation cases.
- Adapter loss regresses readiness without changing the application's
  fail-closed Waycloak routing.

The qBitTorrent manifests under [`examples/qbittorrent`](../../examples/qbittorrent)
are the signed reference packaging pattern.
