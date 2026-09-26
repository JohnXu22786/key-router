#!/usr/bin/env bash
set -euo pipefail

patch=${1-}
case "$patch" in
  0|[1-9]|[1-9][0-9]*) ;;
  *)
    printf 'invalid numeric patch component: %q\n' "$patch" >&2
    exit 1
    ;;
esac

# Increment one decimal digit at a time so valid SemVer components are not
# truncated by the runner's bounded integer arithmetic.
for ((i = ${#patch} - 1; i >= 0; i--)); do
  digit=${patch:i:1}
  case "$digit" in
    [0-8])
      printf '%s%s%s\n' "${patch:0:i}" "$((digit + 1))" "${patch:i+1}"
      exit 0
      ;;
    9)
      patch="${patch:0:i}0${patch:i+1}"
      ;;
  esac
done

printf '1%s\n' "$patch"
