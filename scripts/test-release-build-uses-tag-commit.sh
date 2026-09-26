#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
workflow="$root/.github/workflows/release.yml"

assert_workflow_contains() {
  local expected=$1
  if ! grep -Fq "$expected" "$workflow"; then
    printf 'release workflow is missing expected wiring: %s\n' "$expected" >&2
    exit 1
  fi
}

assert_workflow_contains 'commit_sha: ${{ steps.resolve_commit.outputs.commit_sha }}'
assert_workflow_contains 'commit_sha=$(git rev-parse --verify "${RELEASE_TAG}^{commit}")'
assert_workflow_contains 'echo "commit_sha=$commit_sha" >> "$GITHUB_OUTPUT"'
assert_workflow_contains 'if ! git push origin "$RELEASE_TAG"; then'
assert_workflow_contains 'if ! git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then'
assert_workflow_contains 'git fetch --no-tags --force origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"'
assert_workflow_contains 'ref: ${{ needs.version.outputs.commit_sha }}'
assert_workflow_contains 'Automated release — built from the resolved release tag commit.'

if grep -Fq 'ref: ${{ needs.version.outputs.tag }}' "$workflow"; then
  printf 'release build checks out a moving tag instead of the resolved commit SHA\n' >&2
  exit 1
fi

printf 'release build tag commit checks passed\n'
