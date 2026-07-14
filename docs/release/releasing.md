# Releasing Waycloak

Waycloak releases are created only from protected semantic-version tags by `.github/workflows/release.yaml`. The workflow publishes immutable artifacts to GHCR, signs them keylessly with GitHub OIDC, attaches SPDX SBOM attestations and GitHub build provenance, emits a signed release manifest, verifies every signature, and only then creates the GitHub release.

Versions with a semantic prerelease suffix such as `-alpha.1`, `-beta.2`, or
`-rc.1` are published with GitHub's prerelease classification. Versions without
a suffix are normal releases.

## Prepare a release change

1. Set `version` and `appVersion` in `charts/waycloak/Chart.yaml` and
   `package.version` in `kcl/waycloak/kcl.mod` to the intended version without
   a `v` prefix.
2. Update `PROJECT_STATUS.md`, the roadmap, compatibility statements, and upgrade notes.
3. Regenerate and verify CRDs/RBAC, including the copies embedded in the chart
   and the KCL schemas generated from the same CRDs.
4. Run unit, race, vet, staticcheck, envtest, Kind/k3s acceptance, deterministic Helm rendering/package checks, and secret/vulnerability scans.
5. Merge the reviewed change to the protected default branch with a clean source tree.

The release workflow re-runs these gates. It rejects a tag whose version differs from the source chart.

## Create the protected tag

Create an annotated tag at the reviewed commit, then push only that tag:

```sh
version=v0.3.0 # replace with the prepared chart/KCL version
git tag -s "$version" -m "Waycloak $version"
git push origin "$version"
```

Tag creation and push are maintainer actions; Codex must not perform either without explicit authorization. The GitHub `release` environment should require maintainer approval and restrict deployment branches/tags.

## Published identities

The workflow publishes these multi-architecture image repositories by digest:

- `ghcr.io/amoenus/waycloak-controller`;
- `ghcr.io/amoenus/waycloak-agent`;
- `ghcr.io/amoenus/waycloak-gateway-manager`;
- `ghcr.io/amoenus/waycloak-qbittorrent-adapter`.

The chart is published at `oci://ghcr.io/amoenus/charts/waycloak`. Before packaging, the workflow writes the exact released controller, agent, gateway-manager, and adapter image identities into the chart defaults. It never publishes `latest`.

The chart package contains the `VPNGateway`, `VPNWorkload`, and
`PortForwardLease` CRDs under `crds/`, so a Helm install creates the API before
the controller resources. The optional KCL authoring module is published
separately at `oci://ghcr.io/amoenus/waycloak-kcl`; its version matches the
chart and its generated schemas are verified against those same CRDs.

`release-manifest.json` follows [the release manifest schema](manifest.schema.json). Its Sigstore bundle, chart package, digest-resolved `qbittorrent-example.yaml`, and filesystem/image SBOMs are attached to the GitHub release. The workflow renders that example with the exact adapter reference recorded in the signed manifest and rejects placeholders or mutable images. OCI signatures, SBOM attestations, and provenance remain alongside each registry artifact.

## Verify a release

Use the digest references from the signed manifest, never infer them from a tag:

```sh
version=v0.3.0 # release being verified
identity="https://github.com/Amoenus/waycloak/.github/workflows/release.yaml@refs/tags/$version"

cosign verify \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/amoenus/waycloak-controller@sha256:RELEASE_DIGEST

cosign verify-blob \
  --bundle release-manifest.sigstore.json \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  release-manifest.json
```

Repeat OCI verification for the agent, gateway manager, qBitTorrent adapter,
Helm chart, and KCL module identities recorded in the manifest. GitHub
attestations can additionally be verified with `gh attestation verify` against
this repository. After verifying the KCL tag-to-digest identity, commit the
consumer's generated `kcl.mod.lock`; never track an unverified moving tag.

## Security-tool provenance

CI does not execute mutable Trivy or Gitleaks actions. `hack/install-security-tools.sh` downloads fixed releases directly and verifies hardcoded SHA-256 checksums before execution. Updating either tool requires reviewing its upstream security advisory state and changing both version and checksum intentionally.
