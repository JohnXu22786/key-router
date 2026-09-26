#!/usr/bin/env bash
set -euo pipefail

version=${1-}
# Numeric SemVer prerelease identifiers cannot have leading zeroes; identifiers
# containing a letter or hyphen may use zeroes normally. Build metadata stays
# alphanumeric so the updater cannot mistake a hyphen there for prerelease.
semver_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z]+(\.[0-9A-Za-z]+)*))?$'

invalid_version() {
  printf 'invalid release version: %q\n' "$version" >&2
  exit 1
}

# Reject every character outside the release-tag alphabet before applying the
# structural check. This also rejects newlines, which could otherwise make a
# line-oriented validator accept only the first line of a malicious value.
case "$version" in
  ''|*[!A-Za-z0-9.+-]*)
    invalid_version
    ;;
esac

if [[ ! "$version" =~ $semver_re ]]; then
  invalid_version
fi

# The updater compares core components using the platform's signed 64-bit int.
# Keep accepted release numbers within that same range.
max_component=9223372036854775807
core=${version#v}
core=${core%%[-+]*}
IFS='.' read -r major minor patch <<< "$core"
for component in "$major" "$minor" "$patch"; do
  if (( ${#component} > ${#max_component} )) ||
    { (( ${#component} == ${#max_component} )) && [[ "$component" > "$max_component" ]]; }; then
    invalid_version
  fi
done

if ! git check-ref-format --allow-onelevel "refs/tags/$version" >/dev/null 2>&1; then
  invalid_version
fi

printf '%s\n' "${version#v}"
