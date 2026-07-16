# Minimal independent adapter

[`adapter.py`](adapter.py) implements the public v1alpha1 protocol using only
the Python standard library. It imports no Waycloak package. The sample treats
an atomically replaced `listen-port` file as its application's configuration,
reads it back as the application observation, posts the exact acknowledgement,
and exposes readiness on loopback port 9811.

It is deliberately a teaching implementation rather than a published image.
Production adapters should use application-native APIs, verify the real
listener, add jitter to the bounded backoff, handle graceful shutdown, and
follow the packaging and security checklist in the
[authoring guide](../../docs/guides/workload-adapter-authoring.md).

Run it in an unprivileged sidecar with a writable application-owned `emptyDir`
at `/application`. Admission supplies `WAYCLOAK_ADAPTER_PROTOCOL` and
`WAYCLOAK_LEASE_ENDPOINT`; set `WAYCLOAK_LEASE_NAME` when selection by name is
needed.
