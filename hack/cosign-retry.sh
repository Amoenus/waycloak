#!/usr/bin/env bash

set -euo pipefail

readonly max_attempts=5
readonly initial_delay_seconds=5

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  if cosign "$@"; then
    exit 0
  fi

  if ((attempt == max_attempts)); then
    echo "cosign failed after ${max_attempts} attempts" >&2
    exit 1
  fi

  delay_seconds=$((initial_delay_seconds * attempt))
  echo "cosign attempt ${attempt}/${max_attempts} failed; retrying in ${delay_seconds}s" >&2
  sleep "$delay_seconds"
done
