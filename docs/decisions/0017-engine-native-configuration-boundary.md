# ADR 0017: Engine-native configuration boundary

Status: Accepted
Date: 2026-07-15

Implemented by [ADR 0022](0022-native-engine-input-projection.md).

## Context

ADR 0003 selected Gluetun as the first VPN engine behind a capability
interface. The initial `VPNGateway` API nevertheless models a provider name,
protocol, region, and one credential shape, then translates those fields into
a small set of Gluetun environment variables. That was sufficient to prove
Proton/OpenVPN, but it makes Waycloak responsible for an incomplete copy of
Gluetun's provider configuration API.

Gluetun supports multiple providers, protocols, server filters, custom
OpenVPN configurations, WireGuard configuration files, DNS options, and
updater behavior. Reproducing those settings in Waycloak would lag upstream
and incorrectly make provider configuration part of the Waycloak workload
contract.

Waycloak still must control a few settings because the engine shares the
gateway Pod network namespace with the gateway manager. The manager depends on
a deterministic tunnel interface, loopback-only observed-health endpoints,
an authenticated and restricted control surface, a firewall handoff, and one
owner for provider port forwarding.

## Decision

Waycloak owns the lifecycle and integration contract of the VPN engine
container, but consumers configure the engine using that engine's native
configuration model. A future pre-1.0 `VPNGateway` API revision will replace
the provider-shaped convenience fields with references to operator-owned,
engine-native non-secret and secret inputs.

Configuration values remain opaque to the controller except for structural
validation, redaction, and reserved-setting conflict detection. Waycloak does
not copy secret values into a CR, generated ConfigMap, status, event, log, or
protected workload. The engine receives referenced Secret material directly.

Each engine adapter declares:

- supported native input forms and mount locations;
- observed health and capability interfaces;
- reserved settings required by Waycloak;
- settings that are incompatible with a selected Waycloak capability;
- tested engine versions and immutable image identities.

For Gluetun, Waycloak reserves the tunnel interface, loopback health/control
addresses, control-server authorization, firewall post-rule handoff, and the
built-in port-forward loop when a Waycloak port-forward driver owns leases.
All other provider, protocol, server-selection, DNS, updater, and custom
configuration remains Gluetun-native unless a documented security invariant
requires validation or an override.

Provider-specific capabilities remain separate from tunnel configuration. A
gateway may use Gluetun for a supported provider without Waycloak having a
provider driver. Enabling a capability such as Proton NAT-PMP requires the
engine adapter and provider driver to observe a compatible runtime
configuration; desired configuration alone is not readiness.

The engine remains in the gateway Pod's network namespace. This decision does
not add support for an arbitrary remote VPN endpoint, because the manager must
observe and control the same tunnel and firewall namespace.

## Consequences

- Gluetun can add providers and configuration options without corresponding
  Waycloak API fields.
- Generic documentation describes the engine contract; provider examples are
  explicitly examples rather than the product model.
- Operators retain native Gluetun knowledge and configuration portability.
- Waycloak must define safe references, reserved-key validation, migration,
  and status for invalid or incompatible engine configuration.
- The `v1alpha1` provider fields remain only as mutually exclusive migration
  compatibility while native references become the preferred shape.
- Additional engines can use different native input forms behind the same
  observed capability boundary.

## Alternatives rejected

- Mirror every Gluetun setting in the Waycloak CRD: duplicates a fast-moving
  upstream API and makes Waycloak a provider-configuration bottleneck.
- Accept arbitrary container environment and mounts with no reserved keys:
  permits users to disable health, firewall, or ownership invariants silently.
- Require a separately operated remote Gluetun service: loses the shared
  network-namespace observation and enforcement boundary.
- Hardcode one provider as the generic gateway model: contradicts the
  application- and provider-neutral product contract.
