# ADR 0043: VPN engine capabilities and last-resort application adapters

Status: Accepted

Date: 2026-08-13

## Context

Waycloak uses Gluetun as its VPN engine dependency. Gluetun already owns VPN
provider selection, protocol configuration, tunnel establishment, provider
credentials, and the provider compatibility matrix. Reimplementing those
responsibilities as one Waycloak plugin per VPN provider would duplicate
Gluetun and create a second, incomplete provider API.

Optional provider operations such as renewable inbound port mappings still
need stronger identity, observation, withdrawal, and fail-closed semantics than
an engine status file may expose. Applications also differ in how they learn
and advertise an externally assigned port. These are two different extension
boundaries and must not be combined.

## Decision

Waycloak has one required VPN-engine adapter and two optional extension
contracts:

1. The **Gluetun engine adapter** owns native input projection, reserved-setting
   validation, tunnel/DNS/public-egress observation, firewall handoff, and
   discovery of optional engine-local capabilities. VPN provider support remains
   Gluetun's responsibility.
2. A **port-forward capability driver** is selected by the Gluetun adapter from
   observed compatible native configuration. It owns only acquisition, renewal,
   observation, and release of a provider mapping through the established
   tunnel. The first implementation is Proton NAT-PMP. It is not a Proton VPN
   engine adapter.
3. A **workload adapter** is an optional, out-of-process, application-specific
   compatibility component. It owns only applying and acknowledging a neutral
   lease generation through an application API. It never acquires provider
   mappings or programs packet rules.

The generic Waycloak port-forward manager depends only on the engine-selected
`PortForwardCapability`. It owns durable lease identity, Service and exact-Pod
selection, stable application-side port intent, provider-port-to-backend-port
translation, atomic DNAT/return rules, handoff, status, and withdrawal.

The Gluetun adapter currently selects
`gluetun.waycloak.io/proton-natpmp` only when native configuration explicitly
selects `VPN_SERVICE_PROVIDER=protonvpn` and `VPN_TYPE=openvpn`. Unsupported or
ambiguous configuration fails before rendering the optional runtime. The
generic runtime does not import the Proton implementation or expose a
Proton-specific command-line option.

Application integration follows this mandatory preference order:

1. fixed application port with generic Waycloak translation;
2. a standards-based NAT-PMP/PCP/UPnP presentation when the application handles
   the returned external port correctly;
3. the neutral versioned lease record through a file or authenticated local
   protocol;
4. an explicitly selected workload adapter only after compatibility evidence
   proves the generic mechanisms insufficient.

The workload-adapter protocol receives exact lease, target, mapping,
generation, and expiry identity and returns applied/observed/acknowledged or a
typed failure. Application credentials remain workload-owned. An adapter gets
no Kubernetes token, VPN credential, host access, or networking capability.
Adapter failure can withdraw lease readiness but cannot weaken fail-closed
egress.

The controller durably stages each successor handoff generation before any
runtime or adapter side effect. This two-phase boundary is required because a
successful external delivery followed by a conflicting Kubernetes status write
must retry the same generation, never regress behind application state that is
already applied.

Plugin packaging is independent from runtime responsibility. A release may
include signed reference implementations, but artifact presence never activates
a capability. The installer enables only the generic adapter protocol; operator
authored `WorkloadAdapter` and `PortForwardLease` resources select an immutable
implementation. qBittorrent remains the reference last-resort adapter and is
not part of the installer or controller vocabulary.

## Public contract

The frozen `networking.waycloak.io/v1beta1` resources do not change:

- `VPNGatewayClass` advertises implementation capabilities;
- `VPNGateway` requests and observes capabilities for one concrete Gluetun
  configuration;
- `PortForwardLease` expresses provider-neutral Service-backed inbound intent;
- `WorkloadAdapter` is an immutable operator trust record referenced only when
  application acknowledgement is required.

Engine and capability selection are implementation details behind the gateway
class. Provider names, NAT-PMP endpoints, renewal intervals, and application API
semantics must not enter these generic CRDs.

## Consequences

- Gluetun can expand its provider matrix without Waycloak changes for baseline
  fail-closed egress.
- A new provider-specific operation adds a narrow Gluetun capability driver,
  not a new VPN implementation or application adapter.
- Most applications need no workload adapter.
- The Proton NAT-PMP implementation remains separately testable while its
  direct dependency is contained by the Gluetun adapter.
- Conformance tests must reject unsupported native configuration before the
  optional runtime starts and must test capability, packet-rule, delivery, and
  acknowledgement stages independently.

## Alternatives rejected

- One Waycloak VPN plugin per provider: duplicates Gluetun's primary
  responsibility and configuration model.
- Put Proton directly in the generic gateway runtime: leaks a provider-specific
  implementation across the engine boundary.
- Put qBittorrent behavior in the controller or gateway runtime: spreads
  application credentials and proprietary semantics into privileged components.
- Require every application to run an adapter: makes a compatibility escape
  hatch the default product path.
- Rewrite tracker, DHT, or peer payloads in the data plane: encrypted and
  application-specific protocols make this unsafe and non-general.

## Related decisions

- [ADR 0003](0003-gluetun-provider-interface.md)
- [ADR 0013](0013-proton-nat-pmp-ownership-and-observation.md)
- [ADR 0017](0017-engine-native-configuration-boundary.md)
- [ADR 0018](0018-workload-adapter-protocol.md)
- [ADR 0040](0040-service-backed-single-active-port-forwarding.md)
