#!/usr/bin/env bash

set -euo pipefail

if (( $# < 2 )); then
  echo "usage: $0 <release-tag> <asset> [<asset> ...]" >&2
  exit 2
fi

readonly release_tag="$1"
shift
readonly repository="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
readonly max_attempts=8
readonly initial_delay_seconds=5
readonly -a assets=("$@")

if [[ ! "$release_tag" =~ ^v[0-9] ]]; then
  echo "release tag must begin with a numeric v-version: ${release_tag}" >&2
  exit 2
fi
for asset in "${assets[@]}"; do
  if [[ ! -f "$asset" ]]; then
    echo "release asset does not exist: ${asset}" >&2
    exit 2
  fi
done

retry() {
  local description="$1"
  shift
  local attempt delay_seconds
  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    if "$@"; then
      return 0
    fi
    if ((attempt == max_attempts)); then
      echo "${description} failed after ${max_attempts} attempts" >&2
      return 1
    fi
    delay_seconds=$((initial_delay_seconds * attempt))
    echo "${description} attempt ${attempt}/${max_attempts} failed; retrying in ${delay_seconds}s" >&2
    sleep "$delay_seconds"
  done
}

if ! gh release view "$release_tag" --repo "$repository" >/dev/null 2>&1; then
  created=false
  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    create_args=(
      release create "$release_tag"
      --repo "$repository"
      --verify-tag
      --generate-notes
      --title "$release_tag"
    )
    if [[ "$release_tag" == *-* ]]; then
      create_args+=(--prerelease)
    fi
    if gh "${create_args[@]}"; then
      created=true
      break
    fi
    # A concurrent publisher may have won the create race. Treat the release
    # as established only after it is observable by the exact tag.
    if gh release view "$release_tag" --repo "$repository" >/dev/null 2>&1; then
      created=true
      break
    fi
    if ((attempt < max_attempts)); then
      delay_seconds=$((initial_delay_seconds * attempt))
      echo "release creation attempt ${attempt}/${max_attempts} failed; retrying in ${delay_seconds}s" >&2
      sleep "$delay_seconds"
    fi
  done
  if [[ "$created" != true ]]; then
    echo "release creation failed after ${max_attempts} attempts" >&2
    exit 1
  fi
fi

retry "release asset upload" \
  gh release upload "$release_tag" --repo "$repository" --clobber "${assets[@]}"
