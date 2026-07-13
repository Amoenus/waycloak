# Test strategy

Networking claims require packet-level evidence. Unit tests and “Pod Ready” are insufficient.

## Test layers

### Unit tests

- annotation parsing and mutation idempotence;
- stable allocation and quarantine;
- condition transitions and observed generations;
- provider capability decisions;
- lease renewal state machine;
- redaction and error classification;
- nftables/netlink desired-state calculation using fakes.

Run formatting, `go vet`, staticcheck, race tests, and fuzz tests for annotation/config parsing.

### Controller integration tests

Use envtest for:

- CRD validation/defaulting;
- reconciliation and ownership;
- controller restart with persisted resources;
- namespace authorization;
- finalizer behavior;
- status conflicts and retries.

### Cluster end-to-end tests

Use Kind for every pull request where practical and k3d/k3s in scheduled/release validation. Tests should deploy an isolated fake egress gateway before requiring external VPN credentials.

Mandatory scenarios:

1. unannotated Pod is not mutated and reaches the normal egress observer;
2. annotated Pod is injected and reaches a different gateway egress observer;
3. annotated Pod cannot reach the internet before overlay readiness;
4. deleting the gateway blocks egress without exposing normal node IP;
5. terminating the tunnel blocks egress;
6. restarting controller leaves data-plane protection intact;
7. agent repairs deleted owned routes/rules;
8. DNS service discovery and external resolution follow configured policy;
9. webhook outage does not affect unannotated Pods;
10. annotated-but-uninjected Pod is rejected;
11. adding/removing members does not renumber allocations;
12. unrelated nftables rules survive agent setup and cleanup;
13. no provider Secret or ServiceAccount token appears in an application container.

### Port-forward tests

- protocol-faithful NAT-PMP acquisition, paired TCP/UDP mapping, rotation,
  renewal, expiration, release, timeout, and provider-result failures;
- tunnel-interface binding on Linux and rejection of unsupported platforms;
- stable provider internal-port allocation, generation persistence, and
  deletion quarantine across controller restart and membership changes;
- exact serving-gateway observation without gateway Kubernetes credentials;
- TCP and UDP inbound delivery to the correct target;
- deterministic UID/generation/expiry delivery readback, filtered application
  projection, loopback parity, expiration rejection, and renewal without a Pod
  restart;
- no cross-delivery after target deletion/address reuse;
- two or more simultaneous leases when capabilities permit;
- explicit failure when provider only permits one lease;
- qBitTorrent API adapter updates listen port;
- sustained qBitTorrent DHT health through lease renewal;
- Bitmagnet and Loadstone can consume the neutral lease record.

### Failure injection

Capture packets at the protected Pod, node/CNI interface, gateway overlay, and tunnel where possible. Inject:

- gateway Pod deletion;
- gateway node drain;
- tunnel interface removal;
- DNS failure;
- stale desired generation;
- provider API timeout;
- controller/webhook restart;
- CNI packet loss and MTU mismatch.

The key assertion is absence of direct packets, not only expected application errors.

## Credentialed tests

Provider tests run only in protected CI environments or operator-owned clusters with short-lived credentials. Pull requests from forks never receive credentials. Logs and artifacts are redacted, retained minimally, and must not publish residential/provider-linked public IP history.

## Performance tests

Measure gateway CPU/memory, per-agent RSS, throughput, UDP packet loss, DNS latency, reconciliation duration, and disruption during membership changes at 1, 10, and 50 clients. Publish results with node/kernel/CNI/MTU context.

## Release gate

A release cannot rely on manual observation alone. Required suites, artifact verification, supported-platform results, and any accepted failures are attached to the release manifest.
