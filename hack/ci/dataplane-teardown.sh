#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT
set -euo pipefail

# Build before entering isolated namespaces; never alter the host dataplane.
test_binary="$(mktemp)"
trap 'rm -f "$test_binary"' EXIT
go test -tags=e2e -c -o "$test_binary" ./internal/dataplane
unshare --net --mount --propagation private bash -s -- "$test_binary" <<'PROOF'
set -euo pipefail
test_binary="$1"
namespace="waycloak-teardown-$$"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then kill "$server_pid" 2>/dev/null || true; fi
  ip netns del "$namespace"
}
ip netns add "$namespace"
trap cleanup EXIT
ip link add control0 type veth peer name eth0 netns "$namespace"
ip addr add 192.0.2.1/24 dev control0
ip link set control0 up
ip netns exec "$namespace" ip addr add 192.0.2.2/24 dev eth0
ip netns exec "$namespace" ip link set eth0 up
ip netns exec "$namespace" ip link set lo up
python3 -m http.server 18443 --bind 192.0.2.1 >/dev/null 2>&1 &
server_pid=$!
for attempt in {1..50}; do
  if python3 -c 'import socket; socket.create_connection(("192.0.2.1", 18443), .1).close()' 2>/dev/null; then break; fi
  sleep .1
done
ip netns exec "$namespace" env WAYCLOAK_E2E_NETNS=1 \
  KUBERNETES_SERVICE_HOST=192.0.2.1 KUBERNETES_SERVICE_PORT_HTTPS=18443 \
  "$test_binary" -test.run '^TestLockdownDropsDirectPackets$' -test.v -test.count=1 -test.timeout=30s
PROOF
