#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then
  echo "Tag $RELEASE_TAG already exists on remote; using it."
else
  git tag "$RELEASE_TAG"
  if ! git push origin "$RELEASE_TAG"; then
    echo "Tag push failed (likely created by a concurrent run); checking the remote tag."
    if ! git ls-remote --exit-code --refs origin "refs/tags/$RELEASE_TAG" >/dev/null; then
      echo "::error::Failed to push release tag and no remote tag exists: $RELEASE_TAG" >&2
      exit 1
    fi
  fi
fi

git fetch --no-tags --force origin "refs/tags/$RELEASE_TAG:refs/tags/$RELEASE_TAG"
