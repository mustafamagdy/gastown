#!/usr/bin/env bash
# Install Node, pnpm and the AXI agent tools gt uses, user-locally, at the
# versions pinned below (matching the maintainer's main machine).
# Idempotent. Linux x64/arm64. Does not edit shell profiles: tools are linked
# into ~/.local/bin, which must already be on PATH.
set -euo pipefail

NODE_VERSION=v26.9.0
PNPM_VERSION=12.4.1
AXI_TOOLS=(
  az-axi@0.1.4
  chrome-devtools-axi@0.1.39
  gh-axi@0.1.37
  lavish-axi@0.1.86
  quota-axi@0.1.60
  tasks-axi@0.2.6
)

case "$(uname -m)" in
  x86_64) ARCH=linux-x64 ;;
  aarch64 | arm64) ARCH=linux-arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

BIN="$HOME/.local/bin"
NODE_ROOT="$HOME/.local/lib/nodejs"
mkdir -p "$BIN" "$NODE_ROOT"

if [ "$("$BIN/node" --version 2>/dev/null || true)" != "$NODE_VERSION" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  tarball="node-$NODE_VERSION-$ARCH.tar.xz"
  curl -fsSL -o "$tmp/$tarball" "https://nodejs.org/dist/$NODE_VERSION/$tarball"
  curl -fsSL -o "$tmp/SHASUMS256.txt" "https://nodejs.org/dist/$NODE_VERSION/SHASUMS256.txt"
  (cd "$tmp" && grep " $tarball\$" SHASUMS256.txt | sha256sum -c -)
  tar -xJf "$tmp/$tarball" -C "$NODE_ROOT"
  ln -sfn "$NODE_ROOT/node-$NODE_VERSION-$ARCH" "$NODE_ROOT/current"
fi
for b in node npm npx; do ln -sf "$NODE_ROOT/current/bin/$b" "$BIN/$b"; done

if [ "$("$BIN/pnpm" --version 2>/dev/null || true)" != "$PNPM_VERSION" ]; then
  "$BIN/npm" install -g --prefix "$HOME/.local" "pnpm@$PNPM_VERSION" >/dev/null
fi

export PNPM_HOME="$HOME/.local/share/pnpm"
export PATH="$PNPM_HOME/bin:$BIN:$PATH"
mkdir -p "$PNPM_HOME"
"$BIN/pnpm" add -g "${AXI_TOOLS[@]}"
for b in "$PNPM_HOME"/bin/*-axi; do ln -sf "$b" "$BIN/"; done

for t in "${AXI_TOOLS[@]}"; do
  name="${t%@*}"
  printf '%-22s %s\n' "$name" "$("$BIN/$name" --version 2>&1 | head -1)"
done
