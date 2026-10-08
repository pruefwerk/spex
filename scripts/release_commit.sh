#!/usr/bin/env bash
# Report the checked-out release commit, never the workflow's original SHA.
set -euo pipefail
tag="${1:?release tag required}"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  echo 'Invalid release tag' >&2; exit 2
fi
commit="$(git rev-parse HEAD)"
expected="$(git rev-parse "refs/tags/$tag^{commit}")"
if [ "$commit" != "$expected" ]; then
  echo 'Checkout does not match the release tag' >&2; exit 2
fi
printf '%s\n' "$commit"
