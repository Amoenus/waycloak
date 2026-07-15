# Waycloak documentation

## New users

Start here if you found Waycloak and want to evaluate it in your own cluster:

1. [Getting started](getting-started.md) — the shortest path from a VPN
   account to one protected workload
2. [Architecture and ownership](concepts/architecture-and-ownership.md)
3. [Security exceptions](operations/security-exceptions.md)
4. [Gluetun-native configuration](guides/gluetun-native-configuration.md)
5. [Troubleshooting](operations/troubleshooting.md)
6. [Advanced configuration](operations/advanced-configuration.md)
7. [Upgrade and rollback](operations/upgrade.md)
8. [Uninstall](operations/uninstall.md)

The [protected curl example](../examples/curl/README.md) exercises ordinary
private egress. The [qBitTorrent example](../examples/qbittorrent/README.md)
adds the more advanced provider-assigned port workflow.

## Operations

- [Detailed installation and release verification](operations/install.md)
- [Gluetun-native configuration](guides/gluetun-native-configuration.md)
- [Security exceptions](operations/security-exceptions.md)
- [Troubleshooting](operations/troubleshooting.md)
- [Advanced configuration](operations/advanced-configuration.md)
- [Upgrade and rollback](operations/upgrade.md)
- [Uninstall](operations/uninstall.md)
- [Real-provider port-forward acceptance](testing/real-provider-port-forward.md)

## Product and API contract

- [Product requirements](product/PRD.md)
- [Developer experience](product/developer-experience.md)
- [API contract](api/api-contract.md)
- [Threat model](security/threat-model.md)
- [v0.2 release scope](product/release-scope-v0.2.md)

## Technical design

- [Architecture and ownership](concepts/architecture-and-ownership.md)
- [Detailed architecture](architecture/architecture.md)
- [Networking](architecture/networking.md)
- [Dependencies](implementation/dependencies.md)
- [Implementation blueprint](implementation/blueprint.md)
- [Roadmap](implementation/roadmap.md)

## Quality, delivery, and provenance

- [Test strategy](testing/test-strategy.md)
- [Release engineering](release/release-engineering.md)
- [Release procedure](release/releasing.md)
- [Homelab prototype provenance](provenance/homelab-prototype.md)
- [Architecture decision records](decisions/README.md)

When documents conflict, the product PRD and threat model define behavior;
accepted ADRs define implementation choices. Update affected documents
together when a decision changes them.
