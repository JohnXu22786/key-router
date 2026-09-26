#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
workflow="$root/.github/workflows/release.yml"

assert_workflow_line() {
  local expected=$1
  if ! awk -v expected="$expected" '
    {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      sub(/[[:space:]]+$/, "", line)
      if (line == expected) found = 1
    }
    END { exit !found }
  ' "$workflow"; then
    printf 'release workflow is missing expected active line: %s\n' "$expected" >&2
    exit 1
  fi
}

assert_workflow_line 'commit_sha: ${{ steps.resolve_commit.outputs.commit_sha }}'
assert_workflow_line 'commit_sha=$(git rev-parse --verify "${RELEASE_TAG}^{commit}")'
assert_workflow_line 'echo "commit_sha=$commit_sha" >> "$GITHUB_OUTPUT"'
assert_workflow_line 'if ! git push origin "$RELEASE_TAG"; then'
assert_workflow_line 'if ! git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then'
assert_workflow_line 'git fetch --no-tags --force origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"'
assert_workflow_line 'Automated release — built from the resolved release tag commit.'

read -r build_checkout_count pinned_checkout_count < <(awk '
  function finish_step() {
    if (in_step && is_checkout) {
      checkouts++
      if (has_sha_ref) pinned++
    }
    in_step = 0
    is_checkout = 0
    has_sha_ref = 0
  }

  /^  build:$/ { in_build = 1; next }
  in_build && /^  [[:alnum:]_-]+:/ { finish_step(); exit }
  in_build && /^      - / {
    finish_step()
    in_step = 1
    if ($0 ~ /^      - uses:[[:space:]]*actions\/checkout@[^[:space:]]+/) is_checkout = 1
    next
  }
  in_build && in_step && /^        uses:[[:space:]]*actions\/checkout@[^[:space:]]+/ {
    is_checkout = 1
  }
  in_build && in_step && is_checkout && /^          ref: / {
    if ($0 == "          ref: ${{ needs.version.outputs.commit_sha }}") has_sha_ref = 1
  }
  END {
    finish_step()
    printf "%d %d\n", checkouts, pinned
  }
' "$workflow")

if [[ "$build_checkout_count" -eq 0 || "$build_checkout_count" -ne "$pinned_checkout_count" ]]; then
  printf 'all build checkouts must use the resolved release commit SHA (found %s checkouts, %s pinned)\n' \
    "$build_checkout_count" "$pinned_checkout_count" >&2
  exit 1
fi

printf 'release build tag commit checks passed\n'
