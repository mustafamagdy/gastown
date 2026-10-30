#!/usr/bin/env bash
# Rebuild and install gt when the fork's main branch has new commits.
set -euo pipefail
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"
REPO="$HOME/src/gastown"
BRANCH=main
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
if [ "$(git branch --show-current)" != "$BRANCH" ] || [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "skipping: $REPO is not on a clean $BRANCH checkout"; exit 0
fi
if ! git merge --ff-only --quiet "origin/$BRANCH"; then
  echo "skipping: local $BRANCH has diverged from origin/$BRANCH"; exit 0
fi
echo "updating gt: ${INSTALLED:-unknown} -> ${REMOTE:0:8}"
make install
systemctl --user try-restart gt-dashboard.service || true
gt version
