#!/usr/bin/env bash
# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ "$#" -ne 1 ]] || [[ ! "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$ ]]; then
  echo "release tag must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-beta.NUMBER" >&2
  exit 1
fi
