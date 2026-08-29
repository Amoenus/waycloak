# Local-cluster real-provider soak

Stable graduation requires one uninterrupted multi-day observation of the
exact release on the operator's local cluster. qBittorrent is the sole
application canary. Bitmagnet remains at desired replicas zero and is not
deployed, investigated, or used as certification evidence. The procedure does
not add, reimage, or repurpose cluster nodes.

## Evidence boundary

Use one immutable release manifest, exact chart and image digests, one GitOps
revision, and one declared Kubernetes/CNI/runtime/kernel/architecture row for
at least 72 hours. Any release, manifest, image, selected-node architecture, or
canary-intent change starts a new soak epoch. A planned gateway replacement or
provider mapping rotation does not restart the epoch when the exact release and
workload Pod remain unchanged and the transition is fully recorded.

The beta and RC history before this epoch remains valuable lifecycle evidence:
it proves repeated exact transitions, gateway replacement, fail-closed denial,
DNS recovery, provider renewal, and stable qBittorrent identity across several
releases. It does not substitute for the unchanged-artifact multi-day gate.

## Start the redacted collector

Run from the trusted workstation that owns the homelab kubeconfig:

```powershell
$evidence = Join-Path $env:TEMP "waycloak-rc27-qbittorrent-soak.jsonl"
pwsh -File hack/acceptance/real-provider-soak.ps1 `
  -OutputPath $evidence `
  -ExpectedVersion v0.1.0-rc.27 `
  -ExpectedManifestDigest <exact-signed-release-manifest-digest> `
  -DurationHours 72 `
  -IntervalSeconds 60
```

Use a new output path for every epoch. The collector refuses to append to an
existing file. Both expected release-identity parameters are mandatory; the
collector has no stale candidate default. It records booleans, condition reason codes, transition counts,
expiry, handoff generation, and aggregate restart/write observations. It uses
the provider endpoint only in memory for an external TCP check and never writes
the public address or port. It reads no Secret and never logs a credential.

## Required observations

For every sample, retain:

- exact gateway release version and release-manifest digest;
- Argo/GitOps revision at the beginning and end of the epoch;
- gateway `Ready`, `TunnelReady`, and `DNSReady` conditions;
- all `PortForwardLease` programming, rule, delivery, and acknowledgement
  conditions plus provider expiry and handoff generation;
- gateway, adapter, and qBittorrent readiness, UID-change flags, and restart
  counters;
- qBittorrent TCP and UDP listener presence at the current lease port;
- external and cluster DNS results from the qBittorrent container;
- periodic independent TCP reachability without retaining the public endpoint;
  and
- gateway and lease resource-version changes so expected renewal writes can be
  separated from an unbounded reconciliation loop.

At epoch start, end, and after any provider mapping rotation, run the
credential-contained qBittorrent API proof. Record only listener equality,
`connection_status`, DHT node count, DHT/PeX/LSD booleans, random-port/UPnP
booleans, torrent count, and aggregate peer/seed counts. Zero peers or tracker
responses is not by itself a Waycloak failure because inactive torrents and
unavailable UDP trackers are external application state.

At epoch start and end, and after a planned gateway replacement, repeat the
packet evidence:

1. independent public TCP reachability;
2. external UDP arrival on the VPN tunnel and forwarding to the exact overlay
   endpoint;
3. qBittorrent UDP egress from the overlay through the tunnel; and
4. fail-closed withdrawal during gateway loss with zero ordinary-egress match.

Packet diagnostics use an immutable, short-lived privileged diagnostic Pod on
the existing qBittorrent node, enter only the live gateway network namespace,
install no allow rule, and are deleted immediately after capture. They do not
mutate or replace the gateway or workload Pods.

## Acceptance

The epoch passes only when:

- the exact release identity and GitOps declaration remain unchanged;
- no direct-egress packet or silent fallback is observed;
- every non-ready interval has a bounded condition/log timeline and preserves
  denial; no recurring outage remains unexplained;
- no identity collision, stale `Ready`, wrong-Pod delivery, or unacknowledged
  mapping occurs;
- qBittorrent keeps its Pod UID and has no unexplained restart;
- TCP/UDP listeners follow every acknowledged mapping, external TCP remains
  reachable, and sampled UDP reaches the exact overlay endpoint;
- lease expiry continues to advance and any provider rotation completes one
  ordered withdrawal/program/delivery/acknowledgement handoff;
- cluster and external DNS remain healthy except for explained infrastructure
  loss that Waycloak reports and fails closed; and
- resource-version changes are bounded by actual intent, status transition, or
  provider renewal rather than an unbounded write loop.

Do not waive a failed sample by averaging it away. Diagnose it, attach the
bounded timeline to issues #116 and #141, correct the cause if it is within
Waycloak, and start a new unchanged-artifact epoch when the candidate changes.
