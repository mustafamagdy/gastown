# Multi-org, multi-project architecture

Status: in progress (fork `mustafamagdy/gastown`). This document is the source
of truth for the design; the "Build phases" table at the end tracks what is
implemented.

## Goals

Run Gas Town across several organizations, each with many projects, where one
project can be a large set of git repositories (for example a product with
about 120 microservice repos), and features and branches span those repos.

- Organizations never share agents, issues, identity or credentials.
- A project is the unit of knowledge: agents working on any repo of a project
  share its decisions, service map and memory.
- A feature that touches several repos is planned, built, reviewed and landed
  as one unit.
- The human reviews plans, designs and results in the browser (lavish) and the
  feedback flows back to the right agent.
- Agents use AXI tools; gt's own code uses stable machine interfaces.

Non-goal: firstmate. gt replaces it; nothing in this design depends on it.

## Terminology

| Term | Meaning |
|---|---|
| Org | An ownership boundary (`mbg`, `saas`, `shared`, an external client). |
| Town | One gt install. **One town per org**, at `~/work/<org>/gt`. |
| Project | A named group of rigs inside a town, with shared knowledge and one merge queue. New in this fork. |
| Rig | One git repository registered in the town. Belongs to exactly one project. |
| Feature | An epic bead in the project's beads, spanning one or more rigs. |
| Integration branch | Shared branch for a feature. For a multi-repo feature, the same name in every affected repo. |

Agents (mayor, deacon, boot, dogs, witness, refinery, polecats, crew) keep
their upstream meanings; see `docs/glossary.md`.

## Layers

```
TOWN (org)                      ~/work/<org>/gt, own Dolt port, own herdr session
├─ HQ                           mayor, deacon, boot, dogs (incl. reviewer dog)
└─ PROJECT                      projects/<name>/project.json
   ├─ project beads             own prefix; features (epics) spanning rigs
   ├─ project brain             brief, service map, agent-brain memory folder
   ├─ witness + refinery        per rig, running only while the rig has work
   └─ RIGS (repos)              started on demand
      └─ polecats / crew        own worktree writable, neighbours read-only
```

A rig that is not listed in any project is its own single-rig project, which
keeps upstream behaviour unchanged.

## Org = town

- Path: `~/work/<org>/gt`, so the org's git identity (`includeIf` by path in
  `~/.gitconfig`) applies to every agent automatically.
- Dolt: each town needs its own port (`gt install --dolt-port 3308`, ...).
- herdr: each town sets `herdr_session` to the org name, so towns never share
  a herdr session and workspace names never collide.
- Org environment: agents start with the org's `.envrc` environment (the
  `orgrun` contract), so tools act as the org's accounts (for example `gh` as
  the org's GitHub user, `az` signed in to the org's Azure DevOps).

## Project

### Definition

`<town>/projects/<name>/project.json`, next to `CONTEXT.md` (shared context
injected by `gt prime`), `directives/<role>.md` (project-level role directives,
between town and rig) and `.beads/` (project beads). Rig membership is recorded
on the rig (`project` in `mayor/rigs.json`), not in this file; agents in a
member rig get `GT_PROJECT`.

```json
{
  "type": "project",
  "version": 1,
  "name": "xelta",
  "prefix": "xe",
  "description": "Xelta B2B platform",
  "docs": "~/work/mbg/xelta/docs",
  "memory": "mbg/xelta",
  "on_demand": true
}
```

Planned for later phases: project-level `merge_strategy`, `vcs_provider` and
`require_visual_review` defaults.

Rig names cannot contain hyphens, dots or spaces (session names are parsed on
`-`), so a repo `orders-svc` becomes the rig `orders_svc`.

Rig-level settings still override project-level ones.

### Project beads

Each project has its own beads database (prefix `xe-`). Features (epics) live
there; their child tasks live in the rig databases and depend on the epic.
Convoys stay in HQ beads and can track children across rigs (upstream
behaviour).

### Witness and refinery: per rig, on demand

Upstream runs a witness and a refinery per rig. With 120 rigs that is 240
always-on agents. The first design merged them into one pair per project, but
about 60 call sites assume one witness and one refinery per rig (session names,
mail addresses, agent beads, daemon maps). The fork instead keeps them per rig
and runs them only while the rig has work (next section), which gives the same
cost with no changes to those call sites. Ordered, linked landing of
multi-repo features (phase 4) is a project-level coordinator on top of the
per-rig refineries, not a replacement for them.

### Rigs on demand

Rigs in a project run their witness and refinery only while they have work.
Registration is cheap: a bare clone shared through local git objects.

- **Switch:** `on_demand` in `project.json` (absent means true;
  `gt project create --on-demand=false` keeps the project's rigs always-on).
  Rigs outside a project behave as upstream: always-on.
- **Witness has work** when one of the rig's polecats has a running session.
  Polecat directories outlive their sessions (finished polecats are kept for
  reuse), so a directory alone is not work.
- **Refinery has work** when a pending `events/refinery/*.event` names the rig
  (`payload.rig`) in the last 15 minutes, or the rig has a running polecat
  (it will submit an MR). Older events are ignored: they are wake signals and
  are not always consumed. Open MR
  beads are not polled on every heartbeat (too costly across ~120 rigs); they
  are checked only before a refinery is stopped.
- **Daemon:** on each heartbeat an on-demand rig's witness/refinery is started
  only if it has work. An agent that has had no work for the idle grace
  period (default 10 minutes, `"on_demand_idle_grace": "10m"` in `mayor/daemon.json`) is
  stopped with the same kill path used for parked rigs, but the rig is not
  parked or docked. The last-work time lives in daemon memory; after a daemon
  restart the clock starts at the first observation, so nothing is killed
  immediately. A witness is never stopped while a polecat still has work assigned
  (working, stalled, stuck or review-needed, even with a dead session), and a
  refinery never while open or in-progress MRs exist (or while a safety stop
  applies). Both are checked only just before stopping.
- **Fast start:** spawning a polecat into an on-demand rig starts its witness
  immediately, and `gt mq submit` starts the refinery before nudging it,
  instead of waiting for the next heartbeat. Failures are logged and the
  daemon retries.
- **`gt up`** skips idle on-demand rigs and prints `on demand (idle)`.
- **Visibility:** `gt rig list` and `gt rig status` show `on demand (idle)`
  instead of `stopped` for idle on-demand rigs.
- **Manual override:** `gt rig start` / `gt rig boot` still start the agents.
  The daemon may stop them again once the grace period passes with no work.

## Project brain (shared knowledge)

1. **Project brief:** what the project is and its agreed decisions. Built
   from the project `docs/` and the agent-brain memory folder
   (`~/.agent-brain/memory/<org>/<project>/`). Injected by `gt prime` for every
   agent working in the project.
2. **Service map (implemented):** which service each repo owns, APIs served
   and called, events published and consumed, database. Code in
   `internal/projectmap`.
   - Stored at `projects/<p>/service-map.json`, with a human readable
     `service-map.md` (table plus edges) next to it.
   - Read from each rig's default branch tip in `<rig>/.repo.git` (ref
     `origin/<default>` if present, else `<default>`), no checkout needed.
     Sources, field by field, highest precedence first: the override file
     `projects/<p>/services/<rig>.toml`; the repo manifest `service.toml` or
     `.gt/service.toml` (`name`, `owns`, `calls`, `publishes`, `consumes`,
     `database`, optional `serves = ["GET /x"]`); OpenAPI (`openapi.yaml|yml|json`
     at the root, `api/` or `docs/`) for the served endpoints; fallback name
     from `package.json`, `go.mod` or the repo name.
   - `calls` may use repo names with hyphens (`billing-svc`); they are
     matched to rigs (`billing_svc`) by rig, repo or service name.
   - `gt project map build <p> [--force] [--rig R]` scans; a rig whose branch
     commit is unchanged is skipped (an override file newer than the map
     also forces a rescan). `gt project map show <p> [--rig R] [--json]`
     prints it.
   - Refresh: the daemon heartbeat runs the incremental build for each project
     at most every 5 minutes (one `git rev-parse` per unchanged rig). Errors
     are logged, never fatal.
   - Neighbours of a rig: rigs it calls, rigs that call it, consumers of its
     events, publishers of events it consumes.
3. **Context per task:** `gt prime` prints the brief, then
   `## Service map: <rig> and its neighbours`: the rig's own entry in full and
   each neighbour with its relation and relevant endpoints or events (at most
   10, then "and N more"). Without a map it prints a hint to run
   `gt project map build <p>`. Never the whole project.
4. **Learnings:** durable facts go to the project memory folder, so agents on
   every repo of the project see them.
5. **agent-brain mapping:** the town writes path rules so every rig path maps
   to `<org>/<project>`, not to the repo name or `gt`.

## Multi-repo features

1. The mayor creates the feature epic in the project beads and uses the
   service map to choose the affected rigs.
2. Every affected repo gets an integration branch with the same name, for
   example `feat/xe-42-tenant-credits`.
3. Child tasks are ordered by the service map: contracts and schema first,
   then producers, consumers, UI. Independent children run in parallel.
4. Each polecat writes to its own worktree and gets read-only checkouts of the
   neighbouring repos. A contract change also runs the consumers' tests.
5. When every child is done, the project refinery opens linked PRs in every
   repo, in dependency order, and they land as a set. A rejected PR holds the
   others.

## Review gate (lavish)

`lavish-axi` opens an agent's HTML page in the browser, the human annotates it,
and `lavish-axi poll` returns the feedback.

- `gt review request <html> --bead <id> --kind plan|design|question|approval`
  opens a lavish session in the background, stores the session id on the bead
  and sets the bead to awaiting review. The agent does not block.
- The human gets one HTTPS link (Tailscale Serve in front of lavish; raw HTTP
  links fail in browsers for `ts.net` names).
- The **reviewer dog** polls all open sessions of the town. Feedback is
  recorded on the bead, then nudged to the owning agent if alive, or re-slung
  to a fresh polecat with the page and notes.
- Approve / Reject / Answer are lavish question controls, so results are
  structured.
- With `require_visual_review`, the refinery does not complete a PR until the
  review is approved.

## Tools policy

| Caller | Uses | Why |
|---|---|---|
| Agents | AXI tools (`az-axi`, `gh-axi`, `chrome-devtools-axi`, `lavish-axi`, `quota-axi`) | token-efficient output, next-step hints, no prompts |
| gt Go code | `az ... --output json`, `gh ... --json`, REST APIs | complete data, stable format, separate errors, testable |

AXI output is truncated, minimal and free to change (pre-1.0, TOON format), so
gt code never parses it. Exceptions where no stable layer exists (`quota-axi`,
`lavish-axi poll`) use their machine output mode, pinned versions and contract
tests.

Each town lists the AXI tools it needs (`axi_tools` setting). gt installs them
at pinned versions, merges their session-start context into `gt prime`, and
`gt doctor` checks each one runs (catching, for example, a stale Azure
subscription).

## Runtime routing by quota

The deacon reads `quota-axi` before a polecat starts and picks the agent
runtime by a per-project policy, for example
`{"default": "auto", "hard": "claude", "avoid_below_pct": 20}`. Routine work
goes to the runtime with the most runway; hard work keeps the strongest model.

## Planning sync

Features and stories are planned in the org's tracker (Azure Boards or GitHub
Issues). gt imports a ready feature as a project epic with child beads, and
writes status back (active when a polecat starts, resolved when the PR merges).
Beads is the only working tracker inside gt.

## herdr layout

```
herdr session "<org>"
├─ hq          mayor, deacon, dogs
├─ <project>   one tab per running agent of any rig in the project:
│              "👀 orders_svc", "🔀 orders_svc", "🐱 furiosa@orders_svc"
└─ <rig>       a rig outside any project: its tabs, with polecats as
               linked worktree workspaces nested under it
```

herdr links a worktree workspace only to a workspace of the same repo, so in
a project (many repos) polecats are tabs, not nested worktree workspaces.

## Build phases

| # | Phase | Status |
|---|---|---|
| 0 | Fork host setup in `contrib/fork-host`, this document | done |
| 1 | Project layer: config, `gt project`, project beads, prime context, rigs on demand, herdr grouping | done (tested on the `contrib/project-demo` shop project) |
| 2 | Org identity in agents, per-town AXI tool list, `gt doctor` checks | planned |
| 3 | Project brain: brief, service map, read-only neighbours | service map done |
| 4 | Multi-repo features: same-name integration branches, ordered linked PRs, Azure DevOps PR provider | planned |
| 5 | Review gate: `gt review`, reviewer dog, refinery approval gate | planned |
| 6 | Runtime routing by quota | planned |
| 7 | Planning sync with Azure Boards / GitHub | planned |
| 8 | AXI-style output for `gt` | planned |
