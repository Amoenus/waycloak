# Waycloak Helm chart

This chart installs the clean-break replacement: the six
`networking.waycloak.io/v1beta1` CRDs, persona and runtime RBAC, stable admission
policies, replacement controller, mandatory chained-CNI installer, privileged
node agent, and optional exact default gateway class. It rejects alpha Pod
annotations, malformed enrollment labels, live-Pod enrollment mutation,
host-namespace/direct-node bypass, and schedules enrolled Pods only to nodes
with authenticated current baseline capability.

When `defaultGatewayClass.enabled=true`, the chart renders the tested Gluetun
class only from an exact release version and `sha256` manifest digest supplied
by the verified install plan. The development defaults do not invent a release
identity. Gateway manifests contain no Waycloak image digest.

An existing default class with another exact release identity makes a connected
Helm render fail before mutation. Argo CD cannot use Helm's cluster-backed
lookup, so the class has an earlier sync wave and a raw release sync fails on
that immutable object before executable components advance. Perform upgrades
and rollbacks with reviewed `waycloakctl install plan/apply`, then let GitOps
converge the already matching target; never manually delete or mutate the class.

It does not render the alpha controller, mutation webhooks, sidecars, init
containers, allocation ConfigMaps, alpha CRDs, or any compatibility bridge.
Install through a signed `waycloakctl` exact-artifact plan: development values
do not invent image digests, release identity, observation trust, or root-owned
host paths. The runtime agent never writes the host CNI directories.

For `v0.1.0-rc.1`, pull the chart with `helm pull
oci://ghcr.io/amoenus/charts/waycloak --version 0.1.0-rc.1`. Use the exact
digest recorded in `release-manifest.json` for GitOps and inspection. Initial
installation and changed-release activation still require the reviewed
`waycloakctl` transaction; raw Helm consumption does not establish those safety
preconditions.

Provider port forwarding is disabled by default. Candidate testing must use a
reviewed `waycloakctl install plan --enable-port-forwarding` transaction, a new
exact Waycloak release manifest with the complete eight-image inventory, and a
pre-created immutable controller mTLS Secret containing the exact
`spiffe://waycloak.io/replacement-controller` client identity. The chart rejects
partial port-forward configuration. Enabling it adds the specific
`PortForwardServiceSingleActive` capability to the same default class while its
baseline conformance identity remains `Core-v1`; it does not create another
Waycloak product, release channel, or data plane.

The privileged CNI installer is always host-networked and tokenless. Its Pod
sandbox therefore does not invoke the chained plugin that it installs or
upgrades, including while the node-agent socket is absent or the previous
receipt is incompatible. This bootstrap exception does not apply to enrolled
application Pods, which admission rejects if they request a host namespace.

The only replacement enrollment key is the Pod-template label
`networking.waycloak.io/egress-route: <same-namespace-route-name>`. A present
label is fail-closed intent even when the route is not yet accepted or ready.
Removing or changing enrollment requires changing the workload template and
creating a new Pod; the admission policy rejects edits to an existing Pod.

On Kubernetes 1.36, a stable `MutatingAdmissionPolicy` adds the hard
`networking.waycloak.io.node-restriction.kubernetes.io/cni-ready=true` node
selector to enrolled Pods while preserving all user scheduling constraints.
The authenticated controller publishes that protected label only for an exact
release and baseline capability report and expires it after agent loss. Stable
support requires the NodeRestriction admission plugin; the CNI independently
fails closed if admission or a scheduling label is missing, stale, or bypassed.
The chart installs no mutating/validating webhook or admission TLS resources.

Cross-namespace gateway references use `VPNGateway.spec.allowedRoutes`; the baseline
does not install Gateway API `ReferenceGrant`. Namespace labels selected for
authorization must be operator controlled and outside tenant write authority.
See [`cross-namespace-consent.md`](../../docs/security/cross-namespace-consent.md)
before enabling a selector.

```sh
helm lint charts/waycloak
```

CRDs follow Helm's `crds/` lifecycle: they install before namespaced resources and are not deleted during uninstall.

Persona roles are intentionally unbound. Grant workload authorship in each
approved workload namespace with a namespaced `RoleBinding` to the
`waycloak-workload-owner` ClusterRole. Do not use a `ClusterRoleBinding`, which
would grant route and lease authorship in every namespace.

The controller has no Secret permission by default. In each approved gateway
namespace, bind the `waycloak-gateway-secret-reader` ClusterRole to the chart's
controller ServiceAccount with a namespaced `RoleBinding`. Never use a
`ClusterRoleBinding` for this credential-reader role.

Install exactly one Waycloak release per cluster. The CRDs, admission policy,
and fixed persona ClusterRoles are cluster-wide product identities and are not
safe for competing Helm release ownership.

## Operational visibility

The controller exposes bounded aggregate metrics on the Service's named
`metrics` port by default. Disable it with
`observability.metrics.enabled=false`. No namespace, object name/UID, node,
address, endpoint, provider, or credential label is published.

Set `observability.assets.enabled=true` to render optional plain Prometheus
rules and a Grafana dashboard as ConfigMaps. This adds no Prometheus Operator
or Grafana runtime dependency. See
[`observability.md`](../../docs/operations/observability.md) for the stable
metric contract, scrape fragment, privacy boundary, and alert semantics.

OpenTelemetry operational signals are no-op by default. Set
`observability.openTelemetry.otlpEndpoint` to an existing OTLP/HTTP receiver to
enable bounded gateway export; the chart does not install or require a
collector. Queue saturation and export failure never affect readiness. The
fixed-cardinality schema and tuning values are documented in
[`observability.md`](../../docs/operations/observability.md).
