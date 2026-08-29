# Waycloak documentation

## Product documentation

1. [Getting started](getting-started.md)
2. [Use cases](use-cases.md)
3. [Configuration requirements](configuration.md)
4. [Deployable resources and ownership](deployable-resources.md)
5. [Generated v1beta1 API reference](api/v1beta1.md)
6. [Architecture and ownership](concepts/architecture-and-ownership.md)
7. [Threat model](security/threat-model.md)
8. [Release-candidate notes](releases/v0.1.0-rc.30.md)

## Design and implementation authority

- [Stable and turnkey product requirements](product/stable-turnkey-product.md)
- [Kubernetes API maturity and target architecture](architecture/kubernetes-api-maturity.md)
- [Replacement API proposal](api/replacement-api-proposal.md)
- [Dependency-ordered stable product plan](implementation/stable-product-plan.md)
- [Test strategy](testing/test-strategy.md)
- [Replacement ADRs](decisions/README.md)

The implementation is a destructive clean break. There is no conversion, translation, imported runtime state, dual serving, deprecated alias, annotation bridge, or sidecar fallback.

## Implementation evidence

- [Project status](../PROJECT_STATUS.md)
- [Roadmap](implementation/roadmap.md)
- [Stable operational visibility](operations/observability.md)
- [CNI creation-time feasibility](implementation/cni-creation-time-feasibility.md)
- [Node-agent threat-model evidence](implementation/node-agent-threat-model-evidence.md)
- [Alpha removal order](implementation/alpha-removal-order.md)
- [Reviewed alpha inventory](implementation/alpha-removal-inventory.json)
- [Replacement removal-completion policy](implementation/alpha-removal-completion.json)

## As-built evidence

The [alpha PRD](product/PRD.md), [alpha API contract](api/api-contract.md), older ADRs, research, provenance, and release-scope records describe the removed implementation or its teardown inputs. They are retained for evidence and are not product guidance.

The signed RC has executable install, upgrade, rollback, repair, restore,
destructive purge, doctor, verification, and support-bundle workflows. Stable
graduation evidence remains separate from RC availability.
