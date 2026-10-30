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

## Auto-update

`gt-autoupdate.sh` runs every 5 minutes. It fetches `origin/main` and compares
it with the commit the installed `gt` binary was built from (`gt version`). If
they differ, it fast-forwards `~/src/gastown` and runs `make install`.

It skips the update, without failing, when:

- `~/src/gastown` is not on `main`;
- `~/src/gastown` has uncommitted changes to tracked files (do work in a
  worktree, not in the main checkout);
- local `main` has diverged from `origin/main`.

Logs: `journalctl --user -u gt-autoupdate.service`.

## Town settings

Copy `town-settings.example.json` to `<town>/settings/config.json` to run the
town under herdr inside your own herdr session. See
`docs/design/herdr-backend.md` for every setting, and
`docs/design/org-project-architecture.md` for the multi-org layout.

## Keeping up with upstream

```bash
cd ~/src/gastown
git fetch upstream
git merge upstream/main   # resolve, test, then push to origin main
```
