# Threat model

## Security claim

For an opted-in, non-privileged workload on a supported cluster, Waycloak routes external Pod traffic through the selected healthy VPN gateway and blocks that external traffic when the protected path is unavailable.

This is a fail-closed routing guarantee within the boundaries below. It is not a claim of anonymity.

## Protected assets

- the user's residential or node public IP from accidental workload egress;
- VPN credentials and provider configuration;
- integrity of workload-to-gateway membership;
- integrity and confidentiality of port-forward lease control data;
- availability of ordinary cluster workloads not using Waycloak.

## Trusted components

- Kubernetes API server, etcd, scheduler, kubelet, and node administrator;
- Waycloak controller, webhook, agent, and gateway-manager images;
- configured VPN engine and VPN provider;
- cluster CNI and kernel;
- the operator who creates gateways and Secret references.

## Adversaries and failures considered

- accidental application misconfiguration;
- application attempting ordinary outbound routes without elevated capabilities;
- VPN tunnel process crash or degraded provider connection;
- gateway Pod deletion or node loss;
- controller restart;
- stale or reordered membership configuration;
- DNS resolver failure;
- provider port lease expiration or change;
- malformed untrusted workload annotations;
- compromised application container without Linux capabilities or host access.

## Out of scope

- malicious cluster or node administrators;
- compromised kubelet, kernel, CNI, API server, controller, webhook, gateway, or supply chain;
- privileged/host-network workloads or applications with `NET_ADMIN`;
- traffic correlation by the provider or a global observer;
- browser/application fingerprinting and account-level identity;
- provider logging policies;
- leaks outside IP networking, such as application telemetry containing identity;
- protecting cluster-local traffic in `Preserve` mode.

## Primary threats and controls

### Direct-egress fallback

**Threat:** route setup or gateway loss causes the CNI default route to carry traffic directly.

**Control:** install deny rules before route changes, retain them through all failure states, and permit only required overlay/control traffic on the CNI interface. Test with packet capture during forced failures.

### Admission bypass

**Threat:** an annotated Pod starts without injection because the webhook is unavailable or excluded.

**Control:** narrow webhook selectors, versioned injection markers, and a validating control that rejects annotated but uninjected Pods. Installation and uninstall ordering require dedicated tests.

### DNS leakage

**Threat:** resolvers are reached outside the VPN or fallback configuration bypasses the gateway.

**Control:** gateway-routed external DNS, firewall enforcement, and tests covering UDP, TCP fallback, service discovery, and outage behavior.

### Credential propagation

**Threat:** provider credentials are copied into workloads or emitted in status/logs.

**Control:** gateway-only Secret references, minimal RBAC, automatic token mounting disabled, structured log redaction, and secret scanning in CI.

### Cross-tenant gateway use

**Threat:** a namespace selects a gateway it is not authorized to consume or targets another tenant's forwarded port.

**Control:** namespaced gateways initially; future cross-namespace use requires explicit grants. Admission checks authorization before injection. Lease target identity includes namespace and UID.

### Capability escalation

**Threat:** injection grants networking capability to the application.

**Control:** capabilities apply only to the Waycloak agent container. Application security contexts are not broadened. Agent filesystem and API surface are minimized.

### Rule collision or cleanup damage

**Threat:** agent overwrites CNI/application nftables state or cleanup flushes unrelated rules.

**Control:** dedicated tables/chains, stable ownership comments/handles, transactional updates, and tests with pre-existing rules.

### Port-forward hijacking

**Threat:** a workload claims another lease or stale DNAT points to a new Pod reusing an address.

**Control:** UID-bound leases, stable allocator generations, atomic rule replacement, expiry, and target readiness checks.

### Shared-gateway blast radius

**Threat:** one gateway outage interrupts all members.

**Control:** explicit status, Pod disruption controls, safe singleton lifecycle, resource limits, and future operator-defined shards. HPA is not used to clone a tunnel.

## Secret model

`VPNGateway.spec.credentialsSecretRef` names an existing Secret in the gateway namespace. Waycloak reads or mounts only documented keys. It does not create per-workload copies. ESO, SOPS, Sealed Secrets, Vault, or manual Secret management are all compatible because Waycloak consumes the standard Secret API.

Status never contains credential values. Debug bundles redact Secret data, authorization headers, provider account identifiers, and personally identifying public-IP history by default.

## Security acceptance gates

- forced tunnel and gateway loss produce zero direct-egress packets;
- annotated-but-uninjected Pods are rejected;
- unannotated Pods are unaffected by webhook outage;
- application containers gain no capabilities;
- Secrets exist only in the intended gateway scope;
- network rule cleanup preserves unrelated rules;
- image signatures, SBOMs, and provenance verify before release;
- dependency and container scans meet the documented severity policy.
