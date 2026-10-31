#!/usr/bin/env bash
# Offer new upstream commits to the fork as a tested pull request, or as an
# issue when they conflict. Never touches the fork's main branch.
# Usage: gt-upstream-check.sh [--dry-run]
set -euo pipefail
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"
REPO="${GT_FORK_CLONE:-$HOME/src/gastown}"
FORK="${GT_FORK_REPO:-mustafamagdy/gastown}"
STATE="$HOME/.cache/gt-upstream-check"
DRY_RUN=false
[ "${1:-}" = "--dry-run" ] && DRY_RUN=true

mkdir -p "$STATE"
exec 9>"$STATE/lock"
flock -n 9 || { echo "another check is running"; exit 0; }
cd "$REPO"
git config rerere.enabled true
git fetch --quiet origin main
git fetch --quiet upstream main
UP=$(git rev-parse upstream/main)
BASE=$(git rev-parse origin/main)
COUNT=$(git rev-list --count "$BASE..$UP")
if [ "$COUNT" = 0 ]; then
  echo "fork has everything from upstream"
  exit 0
fi
if ! $DRY_RUN && [ "$(cat "$STATE/last-seen" 2>/dev/null)" = "$UP" ]; then
  echo "upstream ${UP:0:8} already handled"
  exit 0
fi

SHORT=${UP:0:8}
BRANCH="upstream-sync/$SHORT"
WORK="$STATE/work"
cleanup() {
  git -C "$REPO" worktree remove --force "$WORK" 2>/dev/null || true
  git -C "$REPO" branch -D "$BRANCH" 2>/dev/null || true
}
cleanup
trap cleanup EXIT
git worktree add --quiet -b "$BRANCH" "$WORK" "$BASE"

COMMITS=$(git log --no-merges --format='- %h %s' "$BASE..$UP" | head -50)
BODY="$STATE/body.md"
if ! git -C "$WORK" merge --no-edit --quiet "$UP" >"$STATE/merge.log" 2>&1; then
  FILES=$(git -C "$WORK" diff --name-only --diff-filter=U)
  git -C "$WORK" merge --abort
  {
    echo "Upstream has $COUNT new commits at $SHORT that do not merge cleanly."
    echo
    echo "Conflicting files:"
    echo "$FILES" | sed 's/^/- /'
    echo
    echo "Commits:"
    echo "$COMMITS"
    echo
    echo "See \"Keeping up with upstream\" in contrib/fork-host/README.md."
  } >"$BODY"
  echo "conflict with upstream $SHORT:"; echo "$FILES"
  if ! $DRY_RUN; then
    gh issue create --repo "$FORK" --title "Upstream sync conflict at $SHORT" --body-file "$BODY"
    echo "$UP" >"$STATE/last-seen"
  fi
  exit 0
fi

# The host has little memory and no swap, so test only what the fork leans on.
export GOMAXPROCS=2 GOFLAGS=-p=2
PACKAGES="./internal/config/... ./internal/beads/... ./internal/projectmap/... ./internal/projectfeature/... ./internal/vcs/... ./internal/herdrmux/... ./internal/rig/..."
RESULT=passed
{
  (cd "$WORK" && go build ./... && go vet $PACKAGES && env -u GT_MULTIPLEXER go test $PACKAGES)
} >"$STATE/test.log" 2>&1 || RESULT=failed
{
  echo "Merges $COUNT new upstream commits up to $SHORT."
  echo
  echo "Build, vet and tests of the fork's main packages: **$RESULT**."
  if [ "$RESULT" = failed ]; then
    echo
    echo '```'
    tail -30 "$STATE/test.log"
    echo '```'
  fi
  echo
  echo "Commits:"
  echo "$COMMITS"
} >"$BODY"
echo "upstream $SHORT merges cleanly, tests $RESULT"
if ! $DRY_RUN; then
  git -C "$WORK" push --quiet --force-with-lease origin "$BRANCH"
  gh pr create --repo "$FORK" --base main --head "$BRANCH" \
    --title "Merge upstream at $SHORT ($RESULT)" --body-file "$BODY"
  echo "$UP" >"$STATE/last-seen"
fi
