#!/usr/bin/env bash
set -euo pipefail

version=${1-}
# Numeric SemVer prerelease identifiers cannot have leading zeroes; identifiers
# containing a letter or hyphen may use zeroes normally. Build metadata is more
# permissive and may contain any nonempty alphanumeric/hyphen identifier.
semver_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'

# Reject every character outside the release-tag alphabet before applying the
# structural check. This also rejects newlines, which could otherwise make a
# line-oriented validator accept only the first line of a malicious value.
case "$version" in
  ''|*[!A-Za-z0-9.+-]*)
    printf 'invalid release version: %q\n' "$version" >&2
    exit 1
    ;;
esac

if [[ ! "$version" =~ $semver_re ]]; then
  printf 'invalid release version: %q\n' "$version" >&2
  exit 1
fi

printf '%s\n' "${version#v}"
