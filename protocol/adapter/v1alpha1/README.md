# Workload-adapter protocol v1alpha1

This directory is the language-neutral source of truth for Waycloak workload
adapters. An adapter is an ordinary unprivileged container in the protected
Pod. It reads the Pod-local lease contract, applies an application-specific
port, verifies the application listener, and acknowledges the exact applied
revision. It never talks to Kubernetes or a VPN provider.

## HTTP contract

The admission webhook supplies these environment variables to the selected
adapter container:

- `WAYCLOAK_ADAPTER_PROTOCOL=networking.waycloak.io/adapter/v1alpha1`
- `WAYCLOAK_LEASE_ENDPOINT=http://127.0.0.1:9809/v1/port-forward/leases`

The endpoint supports:

| Request | Success | Meaning |
| --- | --- | --- |
| `GET $WAYCLOAK_LEASE_ENDPOINT` | `200` | Complete Pod-bound lease document |
| `GET $WAYCLOAK_LEASE_ENDPOINT/{leaseIdentity}` | `200` | One current lease |
| `POST $WAYCLOAK_LEASE_ENDPOINT/{leaseIdentity}/ack` | `204` | Exact acknowledgement accepted |

`404` means the identity is absent. `409` means the acknowledgement is stale,
expired, or does not exactly match the current Pod UID, lease identity,
generation, and application port. `503` means the projected delivery document
is not currently valid. Response bodies are diagnostic only and must not be
parsed.

The JSON schemas are
[`lease-document.schema.json`](lease-document.schema.json) and
[`acknowledgement.schema.json`](acknowledgement.schema.json). Unknown fields
are not part of v1alpha1. Records and protocol arrays are deterministically
sorted; lease identities must be unique.

The lease document retains its established
`networking.waycloak.io/v1alpha1` API version. The v0.3 compatibility contract
requires `publicAddress` alongside `publicPort`; an updated adapter therefore
stays unready behind an older agent that cannot publish that field. Roll out
the release-matched agent before the adapter. Adapter selection and the exact
acknowledgement use `networking.waycloak.io/adapter/v1alpha1`; these two values
are deliberately distinct.

## Required state machine

1. Start unready. Missing, invalid, duplicated, expired, or ambiguous matching
   leases remain unready.
2. Select one lease by exact identity or by an author-documented selector that
   fails on ambiguity.
3. Apply `applicationPort` through the application's local API.
4. Independently observe that the application is listening on that exact port.
5. POST the exact acknowledgement and become ready only after `204`.
6. On a generation, Pod UID, lease identity, public address, application port,
   or expiry change, discard the old revision and regress readiness until the
   new one is applied and acknowledged.
7. Retry local and loopback failures with bounded exponential backoff and
   jitter. A previously proven revision may tolerate a short, documented local
   observation timeout, but expiry, lease loss, identity change, and sustained
   failure must regress readiness.

Adapter failure never removes the Waycloak agent's deny-first routing state.
It only regresses delivery and Pod readiness; Waycloak does not arbitrarily
restart the application.

## Conformance vectors

[`conformance-fixtures.json`](conformance-fixtures.json) enumerates the
required current, rotated, expired, missing, duplicate, wrong-Pod-UID, and
stale-generation cases. The referenced JSON files in `fixtures/` are portable
test vectors and do not require a Go dependency. Timestamps are deliberately
fixed; a test runner sets its clock to the interval represented by each vector.

The repository's tests load these exact files against the agent contract and
the qBitTorrent reference implementation. Third-party implementations should
drive the same vectors through their adapter process and readiness probe.

## OCI metadata

Published adapter images must be immutable multi-architecture OCI indexes and
carry these labels:

- `io.waycloak.adapter.protocol=networking.waycloak.io/adapter/v1alpha1`
- `io.waycloak.adapter.application=<application identity>`
- `io.waycloak.adapter.application.version=<documented compatibility range>`

Release artifacts must include linux/amd64 and linux/arm64 manifests, an SPDX
SBOM, build provenance, and a signature. A cluster operator verifies that
evidence before creating the digest-pinned `WorkloadAdapter` trust object.
