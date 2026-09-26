#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

commit_sha=$(git rev-parse --verify "${RELEASE_TAG}^{commit}")
printf 'commit_sha=%s\n' "$commit_sha" >> "$GITHUB_OUTPUT"
echo "Resolved $RELEASE_TAG to $commit_sha"
