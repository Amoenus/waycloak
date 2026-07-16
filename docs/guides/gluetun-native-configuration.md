# Gluetun-native gateway configuration

Waycloak v0.3 accepts Gluetun's native environment and file configuration
without adding provider-specific fields to `VPNGateway`. Native configuration
has two input forms:

- `engine.config.envFrom` references same-namespace ConfigMaps whose entries
  become Gluetun environment variables;
- `engine.config.files` mounts a same-namespace ConfigMap or Secret read-only
  only into the engine container.

Do not put credentials in a ConfigMap. Waycloak reads non-secret ConfigMaps to
validate reserved keys and calculate an opaque rollout digest. It never reads
a referenced Secret, and neither the gateway manager nor a protected workload
receives native file mounts.

## Proton OpenVPN

This example preserves the v0.2 behavior while using Gluetun-native names:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: gluetun-proton-openvpn
  namespace: private-egress
data:
  VPN_SERVICE_PROVIDER: protonvpn
  VPN_TYPE: openvpn
  SERVER_COUNTRIES: Netherlands
  OPENVPN_USER_SECRETFILE: /run/engine-native/credentials/username
  OPENVPN_PASSWORD_SECRETFILE: /run/engine-native/credentials/password
---
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNGateway
metadata:
  name: proton-eu
  namespace: private-egress
spec:
  engine:
    type: Gluetun
    image: ghcr.io/qdm12/gluetun@sha256:REQUIRED_DIGEST
    config:
      envFrom:
        - name: gluetun-proton-openvpn
      files:
        - secretRef:
            name: proton-openvpn-credentials
          mountPath: /run/engine-native/credentials
  overlay:
    cidr: 172.30.99.0/24
    vni: 7999
    mtu: 1320
  workloadAccess:
    namespaceSelector: {}
```

The referenced Secret retains `username` and `password` keys. When
`ProtonNatPmp` is enabled, the effective native settings must select
`protonvpn` and `openvpn`; the runtime lease observation must also succeed.
Gluetun's own port-forward loop remains disabled because Waycloak owns leases.

## Mullvad WireGuard

Gluetun supports a WireGuard configuration file at
`/gluetun/wireguard/wg0.conf`. Store `wg0.conf` in a Secret and mount the
Secret directory directly at `/gluetun/wireguard`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: gluetun-mullvad-wireguard
  namespace: private-egress
data:
  VPN_SERVICE_PROVIDER: mullvad
  VPN_TYPE: wireguard
  SERVER_CITIES: Riga
---
apiVersion: networking.waycloak.io/v1alpha1
kind: VPNGateway
metadata:
  name: mullvad-riga
  namespace: private-egress
spec:
  engine:
    type: Gluetun
    image: ghcr.io/qdm12/gluetun@sha256:REQUIRED_DIGEST
    config:
      envFrom:
        - name: gluetun-mullvad-wireguard
      files:
        - secretRef:
            name: mullvad-wireguard
          mountPath: /gluetun/wireguard
  overlay:
    cidr: 172.30.100.0/24
    vni: 8000
    mtu: 1320
  workloadAccess:
    namespaceSelector: {}
```

The same file shape can represent a custom WireGuard provider by changing
`VPN_SERVICE_PROVIDER` to `custom` and supplying the native endpoint data in
`wg0.conf`.

## Custom OpenVPN

Set `VPN_SERVICE_PROVIDER=custom`, `VPN_TYPE=openvpn`, and
`OPENVPN_CUSTOM_CONFIG` to an absolute path under a ConfigMap file mount.
Mount certificates and private keys from a separate Secret path, and use only
absolute references from the OpenVPN file. For example:

```yaml
data:
  VPN_SERVICE_PROVIDER: custom
  VPN_TYPE: openvpn
  OPENVPN_CUSTOM_CONFIG: /run/engine-native/openvpn/custom.conf
```

```yaml
config:
  envFrom:
    - name: gluetun-custom-openvpn
  files:
    - configMapRef:
        name: custom-openvpn-profile
      mountPath: /run/engine-native/openvpn
    - secretRef:
        name: custom-openvpn-keys
      mountPath: /run/engine-native/openvpn-secrets
```

## Reserved integration settings

Waycloak rejects ConfigMaps that set `VPN_INTERFACE`, `DNS_ADDRESS`,
`HEALTH_SERVER_ADDRESS`, `PUBLICIP_ENABLED`, `PORT_FORWARD_ONLY`, any
`FIREWALL` setting, any `VPN_PORT_FORWARDING` setting, or either control-server
address/authentication setting. It also rejects file mounts that mask `/dev`,
`/etc/resolv.conf`, `/iptables`, `/run/waycloak`, or the `/gluetun` state root.
Native files must mount below `/gluetun/` or in `/run/engine-native`.
The rejection uses stable `InvalidEngineConfiguration` or
`EngineConfigurationUnavailable` reasons and never includes a configuration
value. If a previously accepted gateway becomes invalid or unavailable, the
controller quarantines it by scaling the gateway StatefulSet to zero; protected
traffic therefore remains fail closed instead of using stale engine settings.
Restoring an accepted configuration returns the StatefulSet to one replica.

## Migration and rollback

The legacy `provider` object and `engine.config` are mutually exclusive.
Migrate in one API update: remove `provider` and add the equivalent native
references. A ConfigMap change updates the StatefulSet template digest and
emits `GatewayRolloutRequired`; delete the singleton gateway Pod only during an
approved fail-closed maintenance window.

Rollback to a pre-v0.3 controller is supported only for the Proton/OpenVPN shape
above when its provider, protocol, single country selector, and
`username`/`password` Secret map losslessly to the legacy `provider` object.
Migrate that gateway atomically and verify it is accepted before downgrading.
Mullvad/WireGuard, custom WireGuard or OpenVPN, and configurations using other
native-only Gluetun settings have no lossless legacy mapping and must remain on
v0.3 or newer. An older controller does not understand `engine.config` and
cannot safely operate those gateways.
