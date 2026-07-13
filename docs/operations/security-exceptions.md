# Security exceptions

Waycloak drops all capabilities by default and adds only the capabilities required by each infrastructure container. Application containers receive no capabilities or provider credentials from Waycloak. Nevertheless, the current data plane cannot satisfy the Kubernetes Pod Security `restricted` profile.

## Protected workload namespaces

The injected `waycloak-prepare` and `waycloak-agent` containers require `NET_ADMIN` inside the Pod network namespace. Kubernetes Pod Security Admission evaluates the whole Pod, so it cannot exempt only those injected containers. A namespace enforced as `restricted` will reject protected Pods.

Where possible, use an admission policy engine to grant `NET_ADMIN` only to the digest-pinned Waycloak image and container names. If Pod Security Admission is the only available mechanism, a `privileged` namespace label is a broad exception affecting every Pod in that namespace:

```sh
kubectl label namespace applications \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/warn=restricted \
  pod-security.kubernetes.io/audit=restricted
```

Treat that namespace as a stronger trust boundary, restrict who may create Pods there, and use separate namespaces for unrelated workloads. Waycloak does not weaken application container security contexts itself.

## Gateway namespace

The gateway Pod runs the VPN engine as root with a narrowly documented capability set, mounts `/dev/net/tun`, and runs the manager with `NET_ADMIN`. Place gateways in a dedicated namespace, enforce least-privilege RBAC on it, and apply the same explicit Pod Security exception there. Provider credentials must exist only in this namespace.

## Network policy and node controls

- Permit UDP 4789 only between protected Pod nodes and gateway Pod nodes. The gateway also enforces an observed-member source allowlist inside its network namespace.
- Do not grant application containers access to the Kubernetes API token or gateway credential Secret.
- Do not add `NET_RAW`, `SYS_ADMIN`, host networking, or privileged mode to Waycloak containers.
- Restrict node-root access. A node administrator can bypass Pod network policy and is outside Waycloak's threat boundary.
- Keep the tested Gluetun and Waycloak image digests fixed until an intentional upgrade.

Review [the threat model](../security/threat-model.md) before enabling the exception in a multi-tenant cluster.
