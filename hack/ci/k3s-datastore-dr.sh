#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

readonly root=/var/lib/waycloak-k3s-dr
readonly data_dir="$root/data"
readonly snapshot_dir="$root/snapshots"
readonly token_file="$root/server-token"
readonly snapshot_path_file="$root/snapshot-path"
readonly cni_digest_file="$root/cni-state.sha256"
readonly cni_state_dir=/var/lib/cni/waycloak-e2e
readonly cni_config="$data_dir/agent/etc/cni/net.d/10-flannel.conflist"
readonly cni_config_backup="$root/10-flannel.conflist.backup"
readonly cni_binary="$data_dir/data/cni/waycloak-cni"
readonly server_kubeconfig="$root/kubeconfig"
readonly client_kubeconfig=/tmp/waycloak-k3s-dr-kubeconfig
readonly runtime_endpoint=unix:///run/k3s/containerd/containerd.sock
readonly unit_name=waycloak-k3s-dr.service
readonly unit_file="/etc/systemd/system/$unit_name"

require_root() {
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    echo "k3s datastore recovery helper must run as root" >&2
    exit 1
  fi
  if [[ "$root" != /var/lib/waycloak-k3s-dr || "$data_dir" != /var/lib/waycloak-k3s-dr/data ]]; then
    echo "refusing an unexpected recovery root" >&2
    exit 1
  fi
}

wait_for_cluster() {
  local deadline=$((SECONDS + 180))
  until KUBECONFIG="$server_kubeconfig" kubectl get --raw=/readyz >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      journalctl --unit "$unit_name" --no-pager --lines 200 >&2 || true
      echo "K3s API did not become ready" >&2
      exit 1
    fi
    sleep 2
  done
  KUBECONFIG="$server_kubeconfig" kubectl wait --for=condition=Ready node --all --timeout=180s
}

publish_client_kubeconfig() {
  test -s "$server_kubeconfig"
  if [[ -z "${SUDO_UID:-}" || -z "${SUDO_GID:-}" ]]; then
    echo "runner identity is unavailable for the client kubeconfig" >&2
    exit 1
  fi
  install -m 0600 -o "$SUDO_UID" -g "$SUDO_GID" "$server_kubeconfig" "$client_kubeconfig"
}

cold_stop_runtime() {
  if [[ ! -S "${runtime_endpoint#unix://}" ]]; then
    echo "K3s containerd socket is unavailable for cold restore" >&2
    exit 1
  fi
  mapfile -t sandbox_ids < <(k3s crictl --runtime-endpoint="$runtime_endpoint" pods --quiet)
  printf 'cold restore will stop and remove %s exact CRI Pod sandboxes\n' "${#sandbox_ids[@]}"
  local sandbox_id
  for sandbox_id in "${sandbox_ids[@]}"; do
    [[ "$sandbox_id" =~ ^[a-f0-9]{64}$ ]] || {
      echo "refusing unexpected CRI sandbox identity $sandbox_id" >&2
      exit 1
    }
    k3s crictl --runtime-endpoint="$runtime_endpoint" stopp "$sandbox_id"
    k3s crictl --runtime-endpoint="$runtime_endpoint" rmp "$sandbox_id"
  done
  if [[ -n "$(k3s crictl --runtime-endpoint="$runtime_endpoint" ps --quiet)" || -n "$(k3s crictl --runtime-endpoint="$runtime_endpoint" pods --quiet)" ]]; then
    echo "cold restore left a CRI application container or Pod sandbox running" >&2
    exit 1
  fi
}

write_cni_digest() {
  local attachment_count
  attachment_count="$(find "$cni_state_dir" -type f -name '*.json' -print | wc -l)"
  if [[ "$attachment_count" -lt 1 ]]; then
    echo "no durable Waycloak CNI attachment exists at the snapshot boundary" >&2
    exit 1
  fi
  test -x "$cni_binary"
  test -s "$cni_config"
  install -m 0600 "$cni_config" "$cni_config_backup"
  {
    printf '%s\0' "$cni_binary" "$cni_config"
    find "$cni_state_dir" -type f -name '*.json' -print0
  } \
    | sort --zero-terminated \
    | xargs --null sha256sum >"$cni_digest_file"
  chmod 0600 "$cni_digest_file"
}

setup() {
  command -v k3s >/dev/null
  command -v kubectl >/dev/null
  if [[ -e "$root" || -e "$unit_file" || -e "$client_kubeconfig" ]]; then
    echo "refusing non-empty K3s recovery fixture paths" >&2
    exit 1
  fi
  install -d -m 0700 "$root" "$data_dir" "$snapshot_dir"
  umask 077
  openssl rand -hex 32 >"$token_file"
  chmod 0600 "$token_file"

  local staged_unit
  staged_unit="$(mktemp)"
  cat >"$staged_unit" <<EOF
[Unit]
Description=Waycloak pinned K3s datastore recovery fixture
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
Environment=K3S_TOKEN_FILE=$token_file
ExecStart=/usr/local/bin/k3s server --cluster-init --data-dir=$data_dir --write-kubeconfig=$server_kubeconfig --write-kubeconfig-mode=0600 --etcd-snapshot-dir=$snapshot_dir --disable=traefik --disable=servicelb
KillMode=process
Delegate=yes
LimitNOFILE=1048576
LimitNPROC=infinity
TasksMax=infinity
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
  install -m 0644 "$staged_unit" "$unit_file"
  rm -f "$staged_unit"
  systemctl daemon-reload
  systemctl start "$unit_name"
  wait_for_cluster
  publish_client_kubeconfig
}

snapshot() {
  test -s "$token_file"
  test -f "$unit_file"
  if [[ -e "$snapshot_path_file" ]]; then
    echo "snapshot was already taken" >&2
    exit 1
  fi
  write_cni_digest
  date +%s >"$root/snapshot-start-epoch"
  K3S_TOKEN_FILE="$token_file" k3s etcd-snapshot save \
    --data-dir="$data_dir" \
    --etcd-snapshot-dir="$snapshot_dir" \
    --name=waycloak-dr
  date +%s >"$root/snapshot-end-epoch"

  mapfile -t snapshots < <(find "$snapshot_dir" -maxdepth 1 -type f -name 'waycloak-dr-*' -print)
  if [[ "${#snapshots[@]}" -ne 1 ]]; then
    echo "expected one exact K3s snapshot, found ${#snapshots[@]}" >&2
    exit 1
  fi
  chmod 0600 "${snapshots[0]}"
  printf '%s\n' "${snapshots[0]}" >"$snapshot_path_file"
  chmod 0600 "$snapshot_path_file"
}

restore() {
  test -s "$snapshot_path_file"
  test -s "$token_file"
  test -s "$cni_digest_file"
  local snapshot resolved_snapshot
  snapshot="$(<"$snapshot_path_file")"
  resolved_snapshot="$(realpath "$snapshot")"
  if [[ "$resolved_snapshot" != "$snapshot_dir"/* || ! -f "$resolved_snapshot" ]]; then
    echo "snapshot path escaped the exact confidential fixture directory" >&2
    exit 1
  fi

  date +%s >"$root/restore-start-epoch"
  cold_stop_runtime
  systemctl stop "$unit_name"
  (cd / && sha256sum --check "$cni_digest_file")
  K3S_TOKEN_FILE="$token_file" timeout 180s k3s server \
    --cluster-reset \
    --cluster-reset-restore-path="$resolved_snapshot" \
    --data-dir="$data_dir" \
    --etcd-snapshot-dir="$snapshot_dir" \
    --disable=traefik \
    --disable=servicelb
  install -m 0600 "$cni_config_backup" "$cni_config"
  (cd / && sha256sum --check "$cni_digest_file")
  if ! find "$data_dir/server/db" -maxdepth 1 -type d -name 'etcd-old-*' -print -quit | grep -q .; then
    echo "K3s restore did not quarantine the replaced datastore as etcd-old" >&2
    exit 1
  fi
  test -f "$data_dir/server/db/reset-flag"
  local quiesce_deadline=$((SECONDS + 30))
  while pgrep --full '^/usr/local/bin/k3s server .*--cluster-reset' >/dev/null; do
    if (( SECONDS >= quiesce_deadline )); then
      echo "one-shot K3s reset process did not quiesce" >&2
      exit 1
    fi
    sleep 1
  done
  systemctl reset-failed "$unit_name" >/dev/null 2>&1 || true
  if ! systemctl start "$unit_name"; then
    systemctl status "$unit_name" --no-pager >&2 || true
    journalctl --unit "$unit_name" --no-pager --lines 200 >&2 || true
    exit 1
  fi
  wait_for_cluster
  publish_client_kubeconfig
  local reset_flag_deadline=$((SECONDS + 30))
  while [[ -e "$data_dir/server/db/reset-flag" ]]; do
    if (( SECONDS >= reset_flag_deadline )); then
      echo "ordinary K3s restart did not consume the one-shot reset flag" >&2
      exit 1
    fi
    sleep 1
  done
  date +%s >"$root/restore-end-epoch"
}

metrics() {
  local snapshot_seconds restore_seconds
  snapshot_seconds=$(( $(<"$root/snapshot-end-epoch") - $(<"$root/snapshot-start-epoch") ))
  restore_seconds=$(( $(<"$root/restore-end-epoch") - $(<"$root/restore-start-epoch") ))
  printf 'snapshot_seconds=%s\nrestore_seconds=%s\n' "$snapshot_seconds" "$restore_seconds"
}

cleanup() {
  systemctl stop "$unit_name" >/dev/null 2>&1 || true
  if [[ -f "$unit_file" ]]; then
    rm -f "$unit_file"
    systemctl daemon-reload
  fi
  if [[ -d "$root" ]]; then
    rm -rf --one-file-system "$root"
  fi
  rm -f "$client_kubeconfig"
}

require_root
case "${1:-}" in
  setup) setup ;;
  snapshot) snapshot ;;
  restore) restore ;;
  metrics) metrics ;;
  cleanup) cleanup ;;
  *) echo "usage: $0 {setup|snapshot|restore|metrics|cleanup}" >&2; exit 2 ;;
esac
