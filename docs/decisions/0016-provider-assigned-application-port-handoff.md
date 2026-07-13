# ADR 0016: Provider-assigned application-port handoff

Status: Accepted
Date: 2026-07-13

## Context

ADR 0015 keeps the ordinary Waycloak target port stable, but the qBitTorrent
compatibility proof showed that qBitTorrent continues to advertise its local
listener after successfully learning a different PCP external port. It must
therefore listen on the current provider public port. The shared gateway still
needs a stable target boundary, and a public-port rotation must never be marked
delivered merely because a ConfigMap changed.

Putting qBitTorrent calls in the controller would make the control plane
application-specific and require application credentials outside the workload.
Letting an unprivileged adapter modify nftables would add capabilities to the
workload integration. Pointing gateway DNAT directly at an adapter-reported
port would also couple core gateway desired state to a proprietary API.

## Decision

`PortForwardLease.spec.target.applicationPortMode` is `Fixed` by default. In
that mode the application listens on the stable `spec.target.port` and the
existing agent observation is sufficient for `Delivered`.

`ProviderAssigned` is the application-neutral exception mode. The neutral
delivery record contains all three explicit values:

- `targetPort`: the stable gateway-to-Pod port;
- `applicationPort`: the current port the application must bind, equal to the
  observed provider public port;
- `applicationPortMode`: `ProviderAssigned`.

A separately packaged adapter changes its application through a Pod-loopback
API, verifies the resulting listener configuration, and POSTs the exact lease
generation and applied application port to the Waycloak agent. The agent
accepts only the current unexpired Pod-UID-bound record. On its next successful
native nftables reconciliation it installs an exact TCP/UDP prerouting DNAT
from the stable target port to the acknowledged application port and only then
exposes that acknowledgement to the controller.

Acknowledgements are memory-only observed state. Agent restart, record expiry,
or generation change clears them and removes the redirect until the adapter
reapplies the exact current generation. The controller requires the observed
applied port to equal the current provider port before `Delivered=True`.

Target selection deliberately does not require whole-Pod readiness in this
mode. It requires the Pod to be Running, the injected Waycloak agent to be
Ready, and the UID-bound overlay allocation to exist. The adapter may therefore
keep its own readiness false until it applies and acknowledges the first lease;
that readiness remains a downstream outcome instead of becoming a circular
prerequisite. `Fixed` mode retains whole-Pod readiness as its eligibility rule.

The gateway also source-translates traffic from the exact UID-bound overlay
target and either its stable or provider-assigned listener port to the lease's
stable provider internal port before general masquerade. New provider internal
ports come from the dynamic/private range specified by ADR 0013. The exact
rule uses explicit source address and port translation; a port-qualified
masquerade rule was rejected because the Linux acceptance test showed that it
preserved the application's original source port. This preserves the provider
mapping for DHT and other listener-originated UDP without granting the
application networking capabilities.

The initial qBitTorrent adapter connects only to loopback, requires an API key
from a workload-owned file, reads only the neutral lease endpoint, receives no
Kubernetes or VPN credentials, and runs without Linux capabilities.

## Consequences

- Ordinary workloads remain operationally unaware of Waycloak after their
  declarative opt-in.
- Provider-assigned applications have a generic exact-generation handoff; the
  core does not know which application implements it.
- qBitTorrent-specific code and credentials remain in a separate sidecar.
- Rotation has a deliberate fail-closed interval while the new application
  port and kernel redirect are being observed.
- A compromised application container can spoof readiness to another process
  in its own Pod through loopback, but cannot acquire another Pod's lease,
  change gateway rules, read VPN credentials, or gain a network capability.
- Persisting an acknowledgement is intentionally rejected because observed
  application and kernel state must be re-established after restart.

## Alternatives rejected

- Change `spec.target.port` on every provider rotation: desired Kubernetes
  intent would become provider-owned churn and the stable boundary would be
  lost.
- Give the adapter `NET_ADMIN`: application-specific code would enter the
  privileged data-plane boundary.
- Put qBitTorrent API calls in the controller or gateway manager: this would
  spread credentials and proprietary semantics into the core.
- Treat file publication as application delivery: it cannot prove that the
  current application listener or local redirect was applied.
- Rewrite tracker and DHT payloads: encrypted and future protocol variants make
  that incomplete and fragile.
