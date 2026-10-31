# Running gt from this fork

How the maintainer's machines run `gt` built from `mustafamagdy/gastown`, so
any change merged to the fork's `main` reaches every machine within minutes.
Everything needed to rebuild the setup from scratch is in this folder.

## Prerequisites

| Tool | Tested version | Why |
|---|---|---|
| Go | 1.27 | builds gt |
| Dolt | 2.6 | beads database server (port 3307) |
| bd (beads) | 1.0.5 | issue tracker; 1.0.0 cannot read the current schema |
| herdr | 0.9.3 | terminal multiplexer (optional, replaces tmux) |
| tmux | 3.4 | default multiplexer |
| Claude Code | 2.1.31x | agent runtime |
| Tailscale | any | remote access to the dashboard and review links |

## Install

```bash
git clone git@github.com:mustafamagdy/gastown.git ~/src/gastown
cd ~/src/gastown
git remote add upstream https://github.com/gastownhall/gastown.git
contrib/fork-host/install.sh
```

`install.sh` builds and installs gt, installs the updater, and enables the
`gt-autoupdate.timer` user timer.

## AXI tools

`install-axi.sh` installs Node, pnpm and the AXI agent tools (`az-axi`,
`chrome-devtools-axi`, `gh-axi`, `lavish-axi`, `quota-axi`, `tasks-axi`)
user-locally at pinned versions, verifying the Node download checksum. It
links everything into `~/.local/bin` and does not edit shell profiles. The
wrapped CLIs are separate: `gh-axi` needs `gh`, `az-axi` needs `az`, and
`chrome-devtools-axi` needs Chrome or Chromium.

To use `pnpm` itself from a shell, set `PNPM_HOME=$HOME/.local/share/pnpm` and
add `$PNPM_HOME/bin` to `PATH`.

## Auto-update

`gt-autoupdate.sh` runs every 5 minutes. It fetches `origin/main` and compares
it with the commit the installed `gt` binary was built from (`gt version`). If
they differ, it builds that commit in a private worktree at
`~/.cache/gt-autoupdate/build` and runs `make install` there. The state of
`~/src/gastown` does not matter, so local work there never stops updates.

Logs: `journalctl --user -u gt-autoupdate.service`.

## Town settings

Copy `town-settings.example.json` to `<town>/settings/config.json` to run the
town under herdr inside your own herdr session. See
`docs/design/herdr-backend.md` for every setting, and
`docs/design/org-project-architecture.md` for the multi-org layout.

## Keeping up with upstream

The fork takes upstream fixes and features through a reviewed pull request, so
nothing upstream can reach a machine until it is merged into the fork's `main`.

`gt-upstream-check.sh` runs daily (`gt-upstream-check.timer`):

1. It fetches `upstream/main`. No new commits means it stops.
2. It merges upstream into a new branch in a private worktree. Merge, not
   rebase: the fork's `main` is published and machines build from it. `rerere`
   is on, so a conflict resolved once stays resolved.
3. If the merge is clean, it builds, vets and tests the packages the fork
   changes most, then pushes `upstream-sync/<sha>` and opens a pull request
   against the fork with the commit list and test result.
4. If the merge conflicts, it opens an issue on the fork listing the upstream
   commits and the conflicting files.

Each upstream commit is handled once (state in
`~/.cache/gt-upstream-check/last-seen`). It never merges to `main`. To run it
by hand: `contrib/fork-host/gt-upstream-check.sh`, or with `--dry-run` to see
the result with no push, pull request or issue.

Resolving a conflict by hand:

```bash
cd ~/src/gastown
git fetch upstream
git switch -c upstream-sync/manual origin/main
git merge upstream/main    # resolve, then run the tests
git push -u origin HEAD    # open a PR against mustafamagdy/gastown, never upstream
```

Pass `--repo mustafamagdy/gastown` to `gh pr create` here: the clone has both
remotes, and `gh` may otherwise pick the upstream repository.

To keep conflicts rare:

- Put fork features in new files and packages, and make a small call from
  upstream code into them. Most of the fork works this way already.
- Files both sides change often are the risk: `internal/cmd/up.go`,
  `internal/tmux/tmux.go`, `internal/cmd/daemon.go`, `internal/cmd/prime.go`.
- Send fixes that are not specific to the fork upstream as pull requests. Each
  one accepted is one less line to carry.
