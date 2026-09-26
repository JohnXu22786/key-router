#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
validator="$root/scripts/validate-release-version.sh"
increment_patch="$root/scripts/increment-release-patch.sh"

assert_valid() {
  local input=$1 expected=$2 actual
  actual=$(bash "$validator" "$input")
  [[ "$actual" == "$expected" ]] || {
    printf 'expected %q to resolve to %q, got %q\n' "$input" "$expected" "$actual" >&2
    exit 1
  }
}

assert_invalid() {
  local input=$1
  if bash "$validator" "$input" >/dev/null 2>&1; then
    printf 'expected invalid version to be rejected: %q\n' "$input" >&2
    exit 1
  fi
}

assert_patch_increment() {
  local input=$1 expected=$2 actual
  actual=$(bash "$increment_patch" "$input")
  [[ "$actual" == "$expected" ]] || {
    printf 'expected patch %q to increment to %q, got %q\n' "$input" "$expected" "$actual" >&2
    exit 1
  }
}

assert_patch_invalid() {
  local input=$1
  if bash "$increment_patch" "$input" >/dev/null 2>&1; then
    printf 'expected invalid patch component to be rejected: %q\n' "$input" >&2
    exit 1
  fi
}

assert_valid 'v0.1.0' '0.1.0'
assert_valid 'v1.2.3-rc.1+build.7' '1.2.3-rc.1+build.7'
assert_valid 'v10.20.30' '10.20.30'
assert_patch_increment '9' '10'
assert_patch_increment '99' '100'
assert_patch_increment '18446744073709551615' '18446744073709551616'
assert_patch_invalid '01'
assert_patch_invalid '12abc'
assert_patch_invalid '12.3'

assert_invalid '1.2.3'
assert_invalid 'v01.2.3'
assert_invalid 'v1.2'
assert_invalid 'v1.2.3-01'
assert_invalid 'v1.2.3-rc.01'
assert_invalid 'v1.2.3+build-7'
assert_invalid 'v1.2.3+foo.lock'
assert_invalid 'v1.2.9223372036854775808'
assert_invalid 'v1.2.3/../../tmp'
assert_invalid 'v1.2.3;echo injected'
assert_invalid 'v1.2.3";echo injected'

marker=$(mktemp)
rm -f "$marker"
payload="v1.2.3\$(touch $marker)"
assert_invalid "$payload"
[[ ! -e "$marker" ]] || {
  printf 'command substitution executed while validating %q\n' "$payload" >&2
  exit 1
}
rm -f "$marker"

newline_payload=$'v1.2.3\n$(touch /tmp/release-version-test-marker)'
assert_invalid "$newline_payload"

if grep -Fq 'if [ -n "${{ inputs.version }}" ]' "$root/.github/workflows/release.yml" ||
  grep -Fq 'tag="${{ inputs.version }}"' "$root/.github/workflows/release.yml"; then
  printf 'release workflow still interpolates the raw version into shell source\n' >&2
  exit 1
fi
grep -Fq 'VERSION_INPUT: ${{ inputs.version }}' "$root/.github/workflows/release.yml"

printf 'release version validation tests passed\n'
