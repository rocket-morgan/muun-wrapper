#!/usr/bin/env bash
# Prints the Apollo base image tag, building Dockerfile.apollo only when the
# tag is missing. The tag changes with the pinned commit or the recipe.
set -euo pipefail

recipe="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)/Dockerfile.apollo"

commit="$(awk -F= '/^ARG APOLLO_COMMIT=/ { print $2; exit }' "$recipe")"
if [[ ! "$commit" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'Dockerfile.apollo must pin APOLLO_COMMIT to a full commit hash\n' >&2
  exit 1
fi

recipe_hash="$(sha256sum "$recipe")"
tag="muun-wrapper-apollo:${commit:0:12}-${recipe_hash:0:8}"

if ! docker image inspect "$tag" >/dev/null 2>&1; then
  # No build context: the recipe fetches everything it needs.
  docker build --progress=plain --tag "$tag" - <"$recipe" >&2
fi

printf '%s\n' "$tag"
