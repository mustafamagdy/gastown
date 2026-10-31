#!/usr/bin/env bash
# Rebuild and install gt when the fork's main branch has new commits.
set -euo pipefail
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"
REPO="$HOME/src/gastown"
BRANCH=main
BUILD="$HOME/.cache/gt-autoupdate/build"
mkdir -p "$HOME/.cache"
exec 9>"$HOME/.cache/gt-autoupdate.lock"
flock -n 9 || { echo "another update is running"; exit 0; }
cd "$REPO"
git fetch --quiet origin "$BRANCH"
REMOTE=$(git rev-parse "origin/$BRANCH")
# Compare against the installed binary, not the clone: pushing from this
# clone leaves it equal to origin while the binary is still old.
INSTALLED=$(gt version 2>/dev/null | grep -o '@[0-9a-f]*' | tail -1 | cut -d@ -f2 || true)
if [ -n "$INSTALLED" ] && [ "$(git rev-parse --short=8 "$REMOTE")" = "$(git rev-parse --short=8 "$INSTALLED" 2>/dev/null)" ]; then
  exit 0
fi
# Build in a private worktree, so a dirty or diverged ~/src/gastown never stops
# updates. A detached HEAD also skips the Makefile's up-to-date check.
if [ ! -e "$BUILD/.git" ]; then
  git worktree prune
  git worktree add --quiet --detach "$BUILD" "$REMOTE"
fi
git -C "$BUILD" checkout --quiet --detach --force "$REMOTE"
git -C "$BUILD" clean -fdq
echo "updating gt: ${INSTALLED:-unknown} -> ${REMOTE:0:8}"
# The host has little memory and no swap: keep the build small.
GOMAXPROCS=2 GOFLAGS=-p=2 make -C "$BUILD" install
systemctl --user try-restart gt-dashboard.service || true
gt version
