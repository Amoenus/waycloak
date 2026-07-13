# ADR 0004: Helm OCI as the primary distribution

Status: Accepted
Date: 2026-07-13

## Context

Users need a universal installer and immutable, verifiable artifacts. KCL is valuable in the originating platform but is not broadly required. The Magnetron repository demonstrates a hardened APKO/Melange, GHCR, Cosign, and KCL OCI workflow.

## Decision

Publish a signed Helm chart as an OCI artifact and publish all images by immutable digest. Publish KCL as a separately signed optional OCI module. Generate SBOM and provenance for every release and sign a manifest that ties all digests together.

## Consequences

- Plain Helm users have a first-class path.
- GitOps tools can consume immutable OCI artifacts.
- KCL users retain a native module without becoming a controller dependency.
- Release automation must verify signatures and tag-to-digest consistency for every artifact.
