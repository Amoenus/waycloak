# Release engineering

Waycloak follows the hardened OCI release pattern proven in the Magnetron project and strengthens it by signing every distributable artifact.

## Artifacts

- multi-architecture controller image;
- multi-architecture agent image;
- multi-architecture gateway-manager image;
- Helm OCI chart;
- optional KCL OCI module;
- SPDX or CycloneDX SBOM for every image and package;
- SLSA-compatible provenance;
- release manifest containing immutable digests and compatibility data.

All artifacts are published by digest. Human-friendly semantic-version tags are aliases, never the source of deployment identity.

## Image construction

- Build Go binaries with reproducible flags and embedded version metadata.
- Package through Melange where needed.
- Assemble minimal Wolfi images with APKO.
- Run as non-root where compatible with required capabilities.
- Use read-only root filesystems, `RuntimeDefault` seccomp, dropped capabilities by default, and add only documented container-specific capabilities.
- Verify entrypoints and payloads before publication.

## Workflow outline

1. typecheck, lint, unit, race, integration, and e2e tests;
2. build amd64 and arm64 artifacts;
3. construct and inspect OCI indexes/media types/platforms/annotations;
4. generate SBOMs and provenance;
5. vulnerability and secret scans;
6. push immutable artifacts to GHCR;
7. keyless Cosign sign images, Helm chart, KCL module, attestations, and release manifest;
8. verify signatures from the registry;
9. verify version tags resolve to recorded digests;
10. create GitHub release with compatibility and upgrade notes.

GitHub Actions are pinned by full commit SHA. Release workflows use GitHub OIDC and minimal permissions; no long-lived signing key is stored.

The implemented tag workflow also pins Helm, Cosign, Kind, and the Kind node image. Trivy and Gitleaks are installed from fixed release assets whose SHA-256 checksums are verified before execution; mutable action tags are not trusted for security scanners. A protected `release` environment provides the human authorization boundary before registry or GitHub Release mutation.

## Release manifest

The signed manifest ties together:

- Git commit and source repository;
- semantic version;
- image/chart/module digests;
- Gluetun tested digest;
- Kubernetes and CNI compatibility results;
- CRD/storage versions;
- required capability and Pod Security profile;
- completed test run identifiers;
- known limitations.

`hack/releasemanifest` emits the deterministic JSON document defined by `manifest.schema.json`. The workflow signs it as a blob, verifies the resulting Sigstore bundle, and publishes both with the release evidence. `hack/releaseprep` writes the just-published image digest identities into the ephemeral packaging worktree without changing the tagged commit or introducing mutable defaults.

## Versioning

Use semantic versioning. Before `v1.0.0`, API changes are possible but must include migration notes. CRD storage changes require conversion or an explicit migration tool before old storage support is removed.

## Supply-chain policy

- No mutable base-image references.
- No unpinned workflow Actions.
- No release from an unprotected or dirty source ref.
- Critical vulnerabilities block release unless documented as unreachable with maintainer sign-off and a time-bounded issue.
- Third-party license inventory accompanies releases.
