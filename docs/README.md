# Waycloak documentation

## Replacement authority

1. [Stable and turnkey product requirements](product/stable-turnkey-product.md)
2. [Kubernetes API maturity and target architecture](architecture/kubernetes-api-maturity.md)
3. [Replacement API proposal](api/replacement-api-proposal.md)
4. [Dependency-ordered stable product plan](implementation/stable-product-plan.md)
5. [Threat model](security/threat-model.md)
6. [Test strategy](testing/test-strategy.md)
7. [Replacement ADRs](decisions/README.md)

The implementation is a destructive clean break. There is no conversion, translation, imported runtime state, dual serving, deprecated alias, annotation bridge, or sidecar fallback.

## Implementation evidence

- [Project status](../PROJECT_STATUS.md)
- [Roadmap](implementation/roadmap.md)
- [CNI creation-time feasibility](implementation/cni-creation-time-feasibility.md)
- [Node-agent threat-model evidence](implementation/node-agent-threat-model-evidence.md)
- [Alpha removal order](implementation/alpha-removal-order.md)
- [Alpha removal inventory](implementation/alpha-removal-inventory.json)

## As-built evidence

The [alpha PRD](product/PRD.md), [alpha API contract](api/api-contract.md), older ADRs, research, provenance, and release-scope records describe the removed implementation or its teardown inputs. They are retained for evidence and are not product guidance.

Install, upgrade, rollback, restore, destructive purge, and support-bundle instructions will be published by issues #138–#140 only after their executable workflows pass. Until then there is no supported turnkey install or destructive migration procedure.
