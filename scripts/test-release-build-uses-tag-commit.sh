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
assert_workflow_contains 'Automated release — built from the resolved release tag commit.'

read -r build_checkout_count pinned_checkout_count < <(awk '
  /^  build:$/ { in_build = 1; next }
  in_build && /^  [[:alnum:]_-]+:/ { exit }
  in_build && /^      - uses: actions\/checkout@v7[[:space:]]*$/ {
    if (in_checkout && has_sha) pinned++
    checkouts++
    in_checkout = 1
    has_sha = 0
    next
  }
  in_build && /^      - / {
    if (in_checkout && has_sha) pinned++
    in_checkout = 0
    has_sha = 0
    next
  }
  in_build && in_checkout && /^          ref: / {
    if ($0 == "          ref: ${{ needs.version.outputs.commit_sha }}") has_sha = 1
  }
  END {
    if (in_checkout && has_sha) pinned++
    printf "%d %d\n", checkouts, pinned
  }
' "$workflow")

if [[ "$build_checkout_count" -eq 0 || "$build_checkout_count" -ne "$pinned_checkout_count" ]]; then
  printf 'all build checkouts must use the resolved release commit SHA (found %s checkouts, %s pinned)\n' \
    "$build_checkout_count" "$pinned_checkout_count" >&2
  exit 1
fi

printf 'release build tag commit checks passed\n'
