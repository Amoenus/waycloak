# CoreDNS gateway-sidecar qualification

Status: source implementation complete; exact-release and live-cluster evidence pending

Reviewed: 2026-08-29

Issue: #244

## Selection

Waycloak selects CoreDNS v1.14.7 for the gateway split-DNS serving role. The
release pins the official multi-platform image index exactly as
`docker.io/coredns/coredns@sha256:7efd3c635b03efd68c4e8398fc45f0d993d0e9ab016f72c1cefb0fd6d01aa286`.
Registry inspection confirmed linux/amd64 manifest
`sha256:2329f5f0e7e79fbe56dcdf11ecc4337ee2476bb251095bc275fe9461cb88a55b`
and linux/arm64 manifest
`sha256:9a631b1e34491f93a35334bc02d8ae190f16224be41689c7f42cc1711a95fe3a`.
The image declares user/group `65532:65532`; Waycloak additionally enforces
non-root execution, a read-only root filesystem, and no privilege escalation.
The official executable carries a `NET_BIND_SERVICE` file capability, so the
container drops all capabilities and adds back only `NET_BIND_SERVICE`; an
empty permitted set makes Linux reject the upstream executable under
`no_new_privs`. CoreDNS still binds only Waycloak's unprivileged port 1053.

The v1.14.7 source release is Apache-2.0, actively maintained, and corresponds
to commit `427fc80ed9ca47f354585eb30a3f1332950856c4`. Every Waycloak release
generates a CoreDNS SPDX asset, scans the exact image for HIGH/CRITICAL fixed
vulnerabilities, verifies amd64 and arm64 availability, and carries the exact
identity in the signed release manifest.

The first RC.27 publication attempt exposed a Trivy version-ordering false
positive rather than a vulnerable CoreDNS build. The official binary embeds
its exact release commit as Go module pseudo-version
`v0.0.0-20260819003913-427fc80ed9ca`; Trivy compares that as older than the
semantic fixed versions and reported nine CoreDNS findings already fixed by
1.14.3 or earlier. CoreDNS itself reports 1.14.7 and the upstream release tag
resolves to the same embedded commit. Two additional findings concern x/mod
module-download and transparency-log verification code, which is not reachable
in this fixed, read-only DNS-serving process.

`security/coredns-v1.14.7.trivyignore.yaml` records only those eleven findings,
expires on 2026-11-27, and is used only when the release workflow scans the
exact selected CoreDNS digest. The scan shows suppressed findings and still
fails for any other fixed HIGH/CRITICAL vulnerability, any other image, a
changed digest, or an expired exception. A dependency refresh must remove or
re-qualify the exception; it is not a repository-wide vulnerability waiver.

## Candidate comparison

| Candidate | Packaging cost | Protocol ownership | Operational fit | Decision |
| --- | ---: | --- | --- | --- |
| Existing Waycloak proxy | no extra image; 391 lines of custom serving code | Waycloak owns UDP/TCP framing, concurrency and forwarding | smallest process count, highest specialist maintenance burden, opens a new upstream socket per request | remove serving code; retain only semantic probing |
| CoreDNS v1.14.7 | 24,253,167 compressed bytes for linux/amd64; one sidecar | maintained `forward` plugin owns UDP/TCP, EDNS0, truncation retry, connection reuse, health checking and concurrency | exact official multi-platform image, declarative split zones, direct non-root execution | selected |
| AdGuard dnsproxy v0.84.1 | requires a separately qualified wrapper/image and configuration surface | maintained DNS proxy library/application | viable alternative but adds a second packaging and integration path without improving Waycloak's semantic contract | qualified fallback, not shipped |

The source comparison is intentionally not treated as live performance proof.
The exact release must still record idle/steady-state/rotation CPU and RSS,
latency under UDP/TCP and large-response load, and behavior for c-ares, musl,
Go, BusyBox, and qBittorrent before #244 closes.

## Cluster-agnostic boundary

The sidecar does not use the CoreDNS `kubernetes` plugin, a kubeconfig, a
ServiceAccount token, or the Kubernetes API. Preflight supplies two generic
values: the cluster DNS suffix and a reachable IPv4 UDP/TCP DNS endpoint.
Queries in that suffix are forwarded only to that endpoint. All other queries
are forwarded only to Gluetun's loopback DNS listener in the shared gateway
network namespace. A cluster may therefore run CoreDNS, kube-dns, NodeLocal DNS,
or another implementation if the observed endpoint implements ordinary DNS on
UDP and TCP port 53 and preserves the required cluster records.

CoreDNS binds only the Waycloak overlay address on port 1053. A gateway-agent
init invocation establishes the overlay and initial deny rules before the
sidecar starts. The sidecar has bounded forwarding concurrency (`128`), reused
upstream connections, a `5m` CPU/`16Mi` memory request, and a `100m` CPU/`64Mi`
memory limit. CoreDNS does not cache answers or use an alternate forwarding
path. Gluetun's maintained loopback resolver does cache answers by default;
therefore the semantic probe proves that the complete workload-visible path
through Gluetun is currently usable, but an individual repeated name is not an
assertion that Gluetun performed fresh upstream recursion for that sample.

## RC.30 DNS-path finding and remediation

The first RC.30 soak epochs are preserved as failed evidence. Under sustained
qBittorrent tracker traffic, CoreDNS recorded bursts of upstream UDP timeouts
only against Gluetun's loopback listener. Gluetun concurrently reported
DNS-over-TLS timeout and TLS-connection failures, and independent external TCP
checks also failed around some bursts. No CoreDNS, Gluetun, gateway-agent,
controller, node-agent, adapter, or qBittorrent container restarted, and the
release, Pod identities, and GitOps revision did not change. This localizes the
failure below CoreDNS's serving implementation, while the simultaneous TCP
evidence prevents attributing every event solely to DNS.

The next candidate keeps the actively maintained CoreDNS and Gluetun
implementations and changes the qualified Gluetun defaults from one DNS-over-TLS
resolver to DNS-over-HTTPS with `cloudflare,google,quad9`. Gluetun owns resolver
selection, encrypted transport, connection handling, caching, and retry. A
native engine ConfigMap may still supply Gluetun's supported
`DNS_UPSTREAM_RESOLVER_TYPE`, `DNS_UPSTREAM_RESOLVERS`, and `DNS_CACHING` values;
Waycloak materializes those values explicitly and otherwise applies the
qualified defaults. This preserves the frozen Kubernetes API and does not add a
resolver library, custom DNS protocol code, direct ordinary-egress fallback, or
readiness hysteresis.

The dependency refresh on 2026-08-29 reconfirmed Gluetun v3.41.3, CoreDNS
v1.14.7, and `golang.org/x/net` v0.58.0 as the latest stable qualified releases.
Gluetun v3.41.3 includes the v3.41.2 DNS-over-TLS pool fixes; the observed
failure therefore is not addressed by upgrading to another published stable
Gluetun version. The new DoH/multi-resolver policy must pass controlled
single-resolver, multi-resolver, DNS-load, tunnel-loss, and recovery tests before
it can replace the failed RC.30 artifact.

RC.31 proved that the multi-resolver DoH configuration removed the earlier
timeout bursts, but its first epoch still observed one syntactically valid UDP
AAAA `SERVFAIL`. CoreDNS correctly relayed that upstream result. Waycloak's
probe retried transport errors but treated the first `SERVFAIL` as terminal,
withdrawing the gateway for 1.183 seconds even though the next observation
succeeded. The successor applies the same existing maximum of three attempts
and three seconds to `SERVFAIL` responses on the required transport. It does
not accept a TCP result as proof of UDP health, retry other unsuccessful RCodes,
or retain readiness after the bounded observation fails. This keeps strict
per-transport semantics without adding a resolver dependency or readiness
hysteresis.

## Readiness and failure behavior

Waycloak, not CoreDNS, remains the readiness authority. The gateway agent probes
the workload-visible overlay listener serially for:

- cluster A over UDP and TCP;
- external A over UDP and TCP; and
- external AAAA over UDP and TCP.

Each query carries EDNS0. A truncated UDP response must succeed through TCP.
The response ID, exact question, and successful RCode are mandatory. One failed
required check immediately clears `DNSReady` and composite readiness and
reinstalls deny rules. Exporter availability, sidecar process health, and
upstream health checks cannot override this result; no hysteresis or direct
fallback exists.

## Remaining release gate

Source tests cover exact Corefile generation, absence of Kubernetes API
coupling, capability and credential isolation, startup ordering, the complete
probe matrix, truncation retry, bounded transient retries, diagnostic classes,
and fail-closed withdrawal. Completion still requires a newly signed exact
release, homelab GitOps deployment, the live client/runtime matrix, DNS leak and
tunnel-loss tests, qBittorrent behavior, renewal and rotation, measured resource
evidence, rollback, and a new unchanged-artifact minimum 72-hour soak.
