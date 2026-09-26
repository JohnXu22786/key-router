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
assert_workflow_line 'if ! git push origin "$RELEASE_TAG"; then'
assert_workflow_line 'if ! git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then'
assert_workflow_line 'git fetch --no-tags --force origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"'
assert_workflow_line 'Automated release — built from the resolved release tag commit.'

version_output_count=$(awk '
  /^  version:$/ { in_version = 1; next }
  in_version && /^  [[:alnum:]_-]+:/ { exit }
  in_version && /^    outputs:$/ { in_outputs = 1; next }
  in_version && /^    [[:alnum:]_-]+:/ { in_outputs = 0 }
  in_version && in_outputs && $0 == "      commit_sha: ${{ steps.resolve_commit.outputs.commit_sha }}" { count++ }
  END { printf "%d\n", count }
' "$workflow")

if [[ "$version_output_count" -ne 1 ]]; then
  printf 'version job must publish the resolve_commit SHA exactly once (found %s)\n' "$version_output_count" >&2
  exit 1
fi

read -r resolve_step_count tag_binding_count peel_count output_write_count < <(awk '
  function finish_step() {
    if (in_step && is_resolve) {
      resolve_steps++
      if (has_tag_binding) tag_bindings++
      if (has_peel) peels++
      if (has_output_write) output_writes++
    }
    in_step = 0
    is_resolve = 0
    has_tag_binding = 0
    has_peel = 0
    has_output_write = 0
  }

  /^  version:$/ { in_version = 1; next }
  in_version && /^  [[:alnum:]_-]+:/ { finish_step(); exit }
  in_version && /^      - / { finish_step(); in_step = 1; next }
  in_version && in_step && /^        id: resolve_commit$/ { is_resolve = 1 }
  in_version && in_step && is_resolve && /^          RELEASE_TAG: / {
    if ($0 == "          RELEASE_TAG: ${{ steps.resolve.outputs.tag }}") has_tag_binding = 1
  }
  in_version && in_step && is_resolve && /^          commit_sha=/ {
    if ($0 == "          commit_sha=$(git rev-parse --verify \"${RELEASE_TAG}^{commit}\")") has_peel = 1
  }
  in_version && in_step && is_resolve && /^          echo / {
    if ($0 == "          echo \"commit_sha=$commit_sha\" >> \"$GITHUB_OUTPUT\"") has_output_write = 1
  }
  END {
    finish_step()
    printf "%d %d %d %d\n", resolve_steps, tag_bindings, peels, output_writes
  }
' "$workflow")

if [[ "$resolve_step_count" -ne 1 || "$tag_binding_count" -ne 1 ||
  "$peel_count" -ne 1 || "$output_write_count" -ne 1 ]]; then
  printf 'resolve_commit must bind the resolved tag, peel it to a commit, and publish that SHA (steps=%s, tags=%s, peels=%s, outputs=%s)\n' \
    "$resolve_step_count" "$tag_binding_count" "$peel_count" "$output_write_count" >&2
  exit 1
fi

read -r build_dependency_count build_checkout_count pinned_checkout_count < <(awk '
  function yaml_scalar(value, quote) {
    sub(/^[[:space:]]*/, "", value)
    quote = substr(value, 1, 1)
    if (quote == "\"" || quote == sprintf("%c", 39)) {
      value = substr(value, 2)
      sub(quote ".*$", "", value)
    } else {
      sub(/[[:space:]]+#.*$/, "", value)
    }
    sub(/[[:space:]]+$/, "", value)
    return value
  }

  function checkout_action(value) {
    value = yaml_scalar(value)
    return value ~ /^actions\/checkout@[^[:space:]]+$/
  }

  function includes_version(value, count, items, i) {
    value = yaml_scalar(value)
    sub(/^\[/, "", value)
    sub(/\]$/, "", value)
    gsub(/[[:space:]]/, "", value)
    gsub(/"/, "", value)
    gsub(sprintf("%c", 39), "", value)
    count = split(value, items, ",")
    for (i = 1; i <= count; i++) {
      if (items[i] == "version") return 1
    }
    return 0
  }

  function finish_step() {
    if (in_step && is_checkout) {
      checkouts++
      if (has_sha_ref) pinned++
    }
    in_step = 0
    is_checkout = 0
    in_with = 0
    has_sha_ref = 0
  }

  /^  build:$/ { in_build = 1; next }
  in_build && /^  [[:alnum:]_-]+:/ { finish_step(); exit }
  in_build && /^    needs:[[:space:]]*/ {
    value = $0
    sub(/^    needs:[[:space:]]*/, "", value)
    if (value == "") {
      needs_block = 1
    } else if (includes_version(value)) {
      needs_version = 1
    }
    next
  }
  in_build && needs_block && /^      - / {
    value = $0
    sub(/^      -[[:space:]]*/, "", value)
    if (includes_version(value)) needs_version = 1
    next
  }
  in_build && needs_block && /^    - / {
    value = $0
    sub(/^    -[[:space:]]*/, "", value)
    if (includes_version(value)) needs_version = 1
    next
  }
  in_build && needs_block && /^    [[:alnum:]_-]+:/ { needs_block = 0 }
  in_build && /^      - / {
    finish_step()
    in_step = 1
    if ($0 ~ /^      - uses:[[:space:]]*/) {
      value = $0
      sub(/^      - uses:[[:space:]]*/, "", value)
      if (checkout_action(value)) is_checkout = 1
    }
    next
  }
  in_build && in_step && /^        with:/ {
    value = $0
    sub(/^        with:[[:space:]]*/, "", value)
    if (value ~ /^\{/) {
      ref_position = index(value, "ref:")
      if (ref_position > 0 && yaml_scalar(substr(value, ref_position + 4)) == "${{ needs.version.outputs.commit_sha }}") {
        has_sha_ref = 1
      }
      in_with = 0
    } else {
      in_with = 1
    }
    next
  }
  in_build && in_step && /^        [[:alnum:]_-]+:/ {
    in_with = 0
    if ($0 ~ /^        uses:[[:space:]]*/) {
      value = $0
      sub(/^        uses:[[:space:]]*/, "", value)
      if (checkout_action(value)) is_checkout = 1
    }
  }
  in_build && in_step && in_with && /^          ref:[[:space:]]*/ {
    value = $0
    sub(/^          ref:[[:space:]]*/, "", value)
    if (yaml_scalar(value) == "${{ needs.version.outputs.commit_sha }}") has_sha_ref = 1
  }
  END {
    finish_step()
    printf "%d %d %d\n", needs_version, checkouts, pinned
  }
' "$workflow")

if [[ "$build_dependency_count" -ne 1 || "$build_checkout_count" -eq 0 ||
  "$build_checkout_count" -ne "$pinned_checkout_count" ]]; then
  printf 'build must depend on version and pin every checkout to its SHA output (needs=%s, checkouts=%s, pinned=%s)\n' \
    "$build_dependency_count" "$build_checkout_count" "$pinned_checkout_count" >&2
  exit 1
fi

printf 'release build tag commit checks passed\n'
