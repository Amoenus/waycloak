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
- for explicitly selected eBPF preview workloads, the Waycloak CNI plugin,
  prepared-node installer, and node agent;
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

**Control:** engine-only Secret file mounts, no controller or manager Secret
read permission, minimal RBAC, automatic token mounting disabled, reserved-key
validation without value reporting, structured log redaction, and secret
scanning in CI.

### Cross-tenant gateway use

**Threat:** a namespace selects a gateway it is not authorized to consume or targets another tenant's forwarded port.

**Control:** namespaced gateways initially; future cross-namespace use requires explicit grants. Admission checks authorization before injection. Lease target identity includes namespace and UID.

### Capability escalation

**Threat:** injection grants networking capability to the application.

**Control:** capabilities apply only to Waycloak networking init and agent
containers. Application security contexts are not broadened. Networking
component filesystem and API surfaces are minimized.

For the optional eBPF preview, privilege moves from a Pod-netns-scoped agent to
prepared-node components. This increases blast radius even when protected Pods
lose their privileged networking sidecar. CNI-directory writes, cgroupfs,
bpffs, namespace entry, host PID, `CAP_BPF`, `CAP_NET_ADMIN`, `CAP_SYS_ADMIN`,
and `privileged: true` are assessed independently and granted only when live
tests prove necessity. Application containers remain capability-free.

### Preview attachment or capability loss

**Threat:** a selected eBPF workload starts before attachment, loses its pinned
link, schedules to a stale-labeled node, or silently changes to sidecar mode.

**Control:** a chained CNI plugin installs UID-owned Pod-parent default-deny
before successful sandbox network `ADD`; failure prevents sandbox startup. A
separate administrator preparation label and executable node observation gate
initial scheduling. The node agent continuously verifies the selected pinned
generation and reports per-Pod health. Missing, severed, foreign, or
unverifiable state remains closed and not ready. Labels are not treated as a
runtime enforcement signal, and fallback is forbidden.

### CNI installation and cleanup damage

**Threat:** preview installation, upgrade, rollback, or garbage collection
breaks Flannel or removes unrelated host/CNI state.

**Control:** installation is opt-in per node, uses immutable architecture-matched
artifacts, atomically extends an observed compatible conflist, retains its exact
preimage, and removes scheduling eligibility before mutation. `ADD`, `CHECK`,
`DEL`, and `GC` use exact Pod UID/generation ownership. Rollback restores the
byte-identical prior chain only after preview workloads have been drained or
explicitly replaced into their declared backend.

### Rule collision or cleanup damage

**Threat:** agent overwrites CNI/application nftables state or cleanup flushes unrelated rules.

**Control:** dedicated tables/chains, stable ownership comments/handles, transactional updates, and tests with pre-existing rules.

### Port-forward hijacking

**Threat:** a workload claims another lease or stale DNAT points to a new Pod reusing an address.

**Control:** UID-bound leases, stable allocator generations, atomic rule replacement, expiry, and target readiness checks.

### Unreviewed workload-adapter execution

**Threat:** a workload annotation causes the privileged admission path to
inject or trust an arbitrary third-party image, or an adapter receives
Kubernetes/VPN credentials or extra host privilege.

**Control:** only an operator-created cluster-scoped `WorkloadAdapter` can
approve a digest and protocol. Workload intent separately names that record and
an existing container whose image must match exactly. Admission requires a
readiness probe, non-root/read-only execution, seccomp, `drop: [ALL]`, no added
capabilities, hostPath, hostPort, device, or projected service-account token.
Application credentials remain workload-owned and explicitly mounted only
where needed.

### Shared-gateway blast radius

**Threat:** one gateway outage interrupts all members.

**Control:** explicit status, a controller-owned `minAvailable: 1` disruption budget for each singleton, resource limits on the admission/controller Deployment, and future operator-defined shards. The budget blocks voluntary eviction but does not claim seamless failover or prevent involuntary node loss. HPA is not used to clone a tunnel.

## Secret model

`VPNGateway.spec.engine.config.files[].secretRef` names an existing Secret in
the gateway namespace. Kubernetes mounts it read-only only into the engine;
Waycloak processes do not read it and do not create per-workload copies. The
legacy `provider.credentialsSecretRef` uses the same engine-only mount boundary
during migration. ESO, SOPS, Sealed Secrets, Vault, or manual Secret management
remain compatible because the Pod consumes the standard Secret API.

Status never contains credential values. Debug bundles redact Secret data, authorization headers, provider account identifiers, and personally identifying public-IP history by default.

## Security acceptance gates

- forced tunnel and gateway loss produce zero direct-egress packets;
- annotated-but-uninjected Pods are rejected;
- unannotated Pods are unaffected by webhook outage;
- application containers gain no capabilities;
- preview CNI failure, node-agent loss, link severing, and node reboot preserve
  default-deny with no backend fallback;
- preview install/rollback preserves the byte-identical unrelated CNI chain and
  leaves no foreign or stale Waycloak pins;
- Secrets exist only in the intended gateway scope;
- network rule cleanup preserves unrelated rules;
- image signatures, SBOMs, and provenance verify before release;
- dependency and container scans meet the documented severity policy.
