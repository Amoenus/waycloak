# Dependencies and responsibility boundaries

Waycloak integrates and qualifies third-party software, but it does not replace
that software's configuration model. The signed release manifest selects exact
artifact digests; upstream documentation remains authoritative for provider,
application, cluster, and telemetry configuration within the boundaries below.

This does not mean Waycloak disclaims integration responsibility. Waycloak owns
the generated integration settings, reserved-value validation, readiness probes,
identity checks, packet programming, and fail-closed behavior. Report a problem
to Waycloak when the exact supported combination does not preserve that contract.

## Runtime and platform dependencies

| Dependency | v1.0.1 role | Configuration owner | Official documentation |
| --- | --- | --- | --- |
| K3s/Kubernetes | API server, scheduling, CRDs, admission, Services, Secrets, and workload runtime | cluster operator | [K3s requirements](https://docs.k3s.io/installation/requirements), [Kubernetes network plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/network-plugins/) |
| Flannel/containerd | certified primary CNI and container runtime row | K3s/cluster operator | [K3s networking](https://docs.k3s.io/networking/basic-network-options) |
| Gluetun `v3.41.3` | VPN engine, provider selection, OpenVPN tunnel, provider port acquisition/renewal, and protected resolver | gateway operator using Gluetun's model; Waycloak projects and validates it | [Gluetun setup](https://github.com/qdm12/gluetun-wiki/blob/main/setup/readme.md), [provider index](https://github.com/qdm12/gluetun-wiki/tree/main/setup/providers), [VPN options](https://github.com/qdm12/gluetun-wiki/blob/main/setup/options/vpn.md), [port forwarding](https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/vpn-port-forwarding.md), [control server](https://github.com/qdm12/gluetun-wiki/blob/main/setup/advanced/control-server.md) |
| Proton VPN | certified provider configuration for the stable row | account/provider owner | [Gluetun ProtonVPN guide](https://github.com/qdm12/gluetun-wiki/blob/main/setup/providers/protonvpn.md), [Proton manual port forwarding](https://protonvpn.com/support/port-forwarding-manual-setup) |
| CoreDNS `v1.14.7` derivative | split-DNS serving inside the gateway Pod | Waycloak generates the Corefile; CoreDNS owns DNS protocol behavior | [CoreDNS manual](https://coredns.io/manual/plugins/), [`forward` plugin](https://coredns.io/plugins/forward/) |
| Kubernetes pause image | short-lived CNI installer helper | release-owned; no user configuration | [Kubernetes source](https://github.com/kubernetes/kubernetes) |

The complete version, license, security-policy, evidence, and review inventory
is machine-readable in
[`dependencies/dependency-inventory.json`](../dependencies/dependency-inventory.json).
The exact images actually deployed are always the digest references in the
verified `release-manifest.json`, not an upstream tag or the versions in this
human-readable table.

## Optional application and observability dependencies

| Dependency | Role | Boundary | Official documentation |
| --- | --- | --- | --- |
| qBittorrent `5.2.3` | evidence-backed reference application requiring provider-assigned listener updates | qBittorrent owns WebUI, TLS, authentication, torrent, DHT, and application behavior; the Waycloak adapter applies and observes only the lease-bound listener | [qBittorrent wiki](https://github.com/qbittorrent/qBittorrent/wiki/), [WebUI API 5.0+](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-%28qBittorrent-5.0%29) |
| OpenTelemetry | optional bounded metrics/traces export | telemetry backend and receiver are operator-owned; export cannot affect readiness | [OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/), [OTLP exporter configuration](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/) |
| Prometheus/Grafana | optional consumption of rendered rules/dashboard assets | operator-owned and not installed as runtime dependencies | [Prometheus documentation](https://prometheus.io/docs/introduction/overview/), [Grafana documentation](https://grafana.com/docs/grafana/latest/) |

## Packaging and authoring tools

| Tool | Role | Boundary | Official documentation |
| --- | --- | --- | --- |
| Helm | primary OCI-packaged installation surface | `waycloakctl` owns initial install and release transitions; Helm can inspect/render the exact chart | [Helm OCI registries](https://helm.sh/docs/topics/registries/) |
| KCL | optional typed authoring for Waycloak custom resources | no controller, installer, credential, or runtime role | [KCL documentation](https://www.kcl-lang.io/docs/) |
| Cosign/GitHub attestations | release signature and provenance verification | operator verifies published evidence before planning | [Cosign verification](https://docs.sigstore.dev/cosign/verifying/verify/), [GitHub artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations) |

## Gluetun configuration boundary

Use Gluetun's provider pages to choose provider-specific credentials, tunnel
type, server filters, and provider port-forward prerequisites. Put secret values
in the gateway's referenced Secret and reviewed non-secret settings in its
native ConfigMap. Waycloak then:

- rejects unsupported or reserved combinations before rendering the gateway;
- adds its required overlay, DNS, health, firewall, and control-server settings;
- observes the tunnel, public egress, DNS, and optional port mapping;
- withdraws readiness and packet rules when those observations fail; and
- never copies provider credentials into protected workloads.

Do not copy Docker Compose examples literally into a `VPNGateway`. In
particular, do not enable Gluetun shell up/down hooks, expose the control server,
or use Gluetun's qBittorrent command example in parallel with a Waycloak
`WorkloadAdapter`. Waycloak uses an authenticated loopback control role and its
own generation/identity protocol so that tunnel or application failure remains
fail closed.

The stable quick path remains the generated Proton/OpenVPN recipe:

```sh
waycloakctl gateway init \
  --namespace media \
  --name private \
  --class gluetun.waycloak.io \
  --config-map private-gluetun \
  --secret proton-credentials \
  --provider protonvpn \
  --protocol openvpn \
  --overlay-cidr 100.96.0.0/16
```

Provider configurations beyond the signed support matrix are evaluation-only
until they receive their own exact conformance evidence. Unsupported intent
must fail visibly; it must never be made to work by weakening the deny path.

## Where to report a problem

- Provider selection, credentials, server filters, or tunnel-specific behavior:
  first compare with the matching Gluetun provider documentation.
- qBittorrent WebUI/API/TLS or torrent behavior independent of Waycloak: use
  qBittorrent documentation and support.
- Exact Waycloak artifacts, integration projection, identity, readiness,
  routing, DNS containment, lease delivery, or ordinary-egress fallback: report
  it in [Waycloak issues](https://github.com/Amoenus/waycloak/issues) with a
  redacted `waycloakctl support-bundle`.
- Cluster networking or runtime behavior outside the certified row: consult the
  distribution/CNI documentation and qualify that environment separately.
