# Bitmagnet adapter

Bitmagnet's DHT server must bind the current provider-assigned public port.
Waycloak therefore ships a narrow adapter instead of putting Bitmagnet logic
in the controller, gateway manager, or provider driver.

The adapter runs as an unprivileged sidecar in the protected Pod. It reads the
provider-neutral lease document from the Waycloak agent's loopback API, writes
the selected port atomically to Bitmagnet's workload-owned `config.yml`,
observes the UDP listener through the shared Pod network namespace, and only
then acknowledges the exact Pod UID, lease identity, generation, and applied
port. It receives no Kubernetes token, VPN credential, or Linux capability.

## Required workload lifecycle

Bitmagnet reads `dht_server.port` only when its process starts. The application
container must therefore use the adapter's `restart-probe` as a liveness probe.
When a provider rotation changes the staged configuration, that probe fails
until Bitmagnet restarts and binds the new UDP port. The adapter's ordinary
`probe` remains unready until the new listener is observed and acknowledged.

```yaml
metadata:
  annotations:
    networking.waycloak.io/gateway: waycloak-system/private-egress
    networking.waycloak.io/workload-adapter: bitmagnet
    networking.waycloak.io/adapter-container: waycloak-bitmagnet-adapter
spec:
  initContainers:
    - name: install-waycloak-bitmagnet-probe
      image: ghcr.io/amoenus/waycloak-bitmagnet-adapter@sha256:RELEASE_DIGEST
      args: [install]
      volumeMounts:
        - name: adapter-bin
          mountPath: /waycloak-adapter-bin
  containers:
    - name: bitmagnet
      livenessProbe:
        exec:
          command: [/waycloak-adapter-bin/bitmagnet-adapter, restart-probe]
      volumeMounts:
        - name: adapter-bin
          mountPath: /waycloak-adapter-bin
          readOnly: true
    - name: waycloak-bitmagnet-adapter
      image: ghcr.io/amoenus/waycloak-bitmagnet-adapter@sha256:RELEASE_DIGEST
      args: [run]
      env:
        - name: WAYCLOAK_LEASE_NAME
          value: bitmagnet
        - name: WAYCLOAK_BITMAGNET_CONFIG_FILE
          value: /root/.config/bitmagnet/config.yml
      readinessProbe:
        exec:
          command: [/ko-app/bitmagnet-adapter, probe]
  volumes:
    - name: adapter-bin
      emptyDir: {}
```

The liveness command executes a read-only copy of the adapter binary through a
dedicated `emptyDir`. The released adapter's `install` mode performs that copy
without requiring a shell in its distroless image. Do not add a
process-namespace share.

`PortForwardLease.spec.target.applicationPortMode` must be
`ProviderAssigned`. Loss, expiry, ambiguity, listener mismatch, or a rejected
acknowledgement makes the adapter unready. Waycloak's injected agent continues
to own fail-closed packet policy independently of the application restart.
