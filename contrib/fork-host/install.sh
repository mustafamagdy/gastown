#!/usr/bin/env bash
# Set up a machine to run gt from this fork and keep it updated.
# Idempotent. Run from a clone of the fork: contrib/fork-host/install.sh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"

if [ "$REPO" != "$HOME/src/gastown" ]; then
  echo "note: the updater expects the clone at ~/src/gastown (this one is $REPO)"
fi

install -Dm755 "$HERE/gt-autoupdate.sh" "$HOME/.local/libexec/gt-autoupdate.sh"
install -Dm644 "$HERE/systemd/gt-autoupdate.service" "$HOME/.config/systemd/user/gt-autoupdate.service"
install -Dm644 "$HERE/systemd/gt-autoupdate.timer" "$HOME/.config/systemd/user/gt-autoupdate.timer"
install -Dm755 "$HERE/gt-upstream-check.sh" "$HOME/.local/libexec/gt-upstream-check.sh"
install -Dm644 "$HERE/systemd/gt-upstream-check.service" "$HOME/.config/systemd/user/gt-upstream-check.service"
install -Dm644 "$HERE/systemd/gt-upstream-check.timer" "$HOME/.config/systemd/user/gt-upstream-check.timer"

(cd "$REPO" && make install)

systemctl --user daemon-reload
systemctl --user enable --now gt-autoupdate.timer gt-upstream-check.timer
# Keep user services running without a login session.
loginctl enable-linger "$USER" 2>/dev/null || echo "run: sudo loginctl enable-linger $USER"
gt version
