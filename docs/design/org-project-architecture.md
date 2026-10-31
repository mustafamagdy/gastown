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
├─ HQ                           mayor, deacon, boot, dogs
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
- Org environment: agents start with the org's environment (the `orgrun`
  contract), so tools act as the org's accounts (for example `gh` as the org's
  GitHub user, `az` signed in to the org's Azure DevOps). Declared by the
  `org_env` town setting, see below.

### Org environment (`org_env`)

`<town>/settings/config.json` names the source of the org environment. Exactly
one of:

- `{"direnv": "<dir>"}`: env is the output of `direnv exec <dir> env -0`.
- `{"file": "<path>"}`: a dotenv file (`KEY=VALUE` lines, `#` comments,
  optional `export `, single or double quotes stripped, no interpolation).

`~` expands in both. An optional `"vars"` allow-list limits what is taken.
Without it, every variable that differs from gt's own process environment is
taken. Never overridden, in either mode: identity and session variables gt
sets (`GT_*`, `BD_*`, `BEADS_*`, `TMUX*`, `HERDR_*`, `CLAUDE_CONFIG_DIR`,
`HOME`, `USER`, `PWD`, `SHLVL`, `_`, `OLDPWD`). `PATH` is merged: entries that
are new are prepended, existing ones are kept.

```json
{
  "type": "town-settings",
  "version": 1,
  "org_env": {
    "direnv": "~/work/mbg",
    "vars": ["GH_TOKEN", "AZURE_DEVOPS_EXT_PAT"]
  },
  "axi_tools": ["gh-axi@0.1.37", "az-axi", "lavish-axi", "quota-axi"]
}
```

The resolved variables are written to `<town>/.runtime/org-env.sh` (directory
0700, file 0600, atomic rewrite, one `export KEY='value'` line per variable).
`config.AgentEnv` only sets `GT_ORG_ENV_FILE=<that path>`; every startup
command builder emits `[ -r '<file>' ] && . '<file>'; ` before gt's own
exports, so every agent session of the town gets the org environment (mayor,
deacon, dogs, witness, refinery, polecats, crew) and gt's own variables still
win. Secret values never appear on a command line (`ps`) or in herdr state.
It is resolved once per gt process and cached, because direnv can be slow. A
failure prints one warning to stderr, leaves any existing file unreferenced,
and agents start without the org environment; agent start is never blocked.
Output carries variable names and counts only, never values.

`gt org env [--json]` prints the source and the resolved variable names.

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

Landing settings (`merge_strategy`, `vcs_provider`, `require_review`) and
`require_visual_review` (default false) are described under "Multi-repo
features" and "Review gate".

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
4. **Project memory and docs in prime (implemented):** after the project
   `CONTEXT.md`, `gt prime` prints `## Project memory (<org>/<project>)`: the
   `MEMORY.md` index of `${AGENT_BRAIN_DIR:-~/.agent-brain}/memory/<memory key>/`
   (at most 40 lines, then "and N more in <dir>") and two lines telling the
   agent to read the linked files when relevant and to save durable project
   facts there (one fact per file plus an index line, never secrets). If
   `docs` is set, up to 15 top-level doc file names follow under
   `Project docs: <dir>`. Nothing is printed when the memory key is unset or
   the folder is missing; prime never fails on this.
5. **Learnings:** durable facts go to the project memory folder, so agents on
   every repo of the project see them.
6. **Read-only neighbour checkouts (implemented):** when a polecat is spawned
   or reused in a project rig, `polecats/<name>/neighbours/<neighbour-rig>`
   gets a detached worktree of each neighbour (from the saved service map, in
   `Neighbours` order) at `origin/<default>` (else `<default>`) of the
   neighbour's `.repo.git`, with write bits removed. Reuse refreshes them to
   the latest commit (or recreates them). `neighbour_checkouts` in
   `project.json` caps the count (default 8, `0` disables). They are removed
   (`git worktree remove --force`, then prune) wherever the polecat is
   removed. Failures only warn. Prime lists them for polecats as
   `Read-only code: ../neighbours/<rig> (<commit>)`.
7. **agent-brain mapping:** `gt project brain-rules <project>` prints one rule
   per rig, `{ path = "~/gt/<rig>", org = "<org>", project = "<project>" }`
   (org and project from the memory key), to paste into agent-brain's
   `config/brain.toml` under a machine's `rules`, so every rig path maps to
   `<org>/<project>` and not to the repo name or `gt`. More specific rules must
   come before generic ones. It modifies no agent-brain file.

## Multi-repo features

A multi-repo feature is one epic in the project beads plus a **feature
record**, a JSON file at `projects/<project>/features/<epic-id>.json`. The
record is the source of truth. Bead edges cannot be: `bd` drops a parent link
whose target lives in another database (a rig child of a project epic reads
back with no parent, so integration branch detection finds nothing and spawn
and `gt done` fall back to main), `bd list --parent` only sees the local
database, and an epic holds a single integration branch and base, while a
feature needs one branch per rig.

The record holds the epic id, project, title, the one branch name used in every
rig, a state (`open`, `landing`, `landed`, `held`), per-rig state (base branch,
base commit, `state`, `pr_url`, `pr_id`, `pr_head`, `landed_commit`, `error`), the
`landing_order` and the list
of tasks (rig bead id, rig, title, `after` list). A task's status is not stored;
it is always read from the bead. Writes are atomic and serialized with a file
lock, so the CLI and the daemon do not lose each other's changes. The code is in
`internal/projectfeature`.

1. `gt project feature create <project> "<title>" --rigs a,b,c [--branch name]`
   creates the epic in the project beads and the same-named branch (default
   `feat/<epic-id>-<slug>`) in every rig. The rigs must belong to the project.
   With `--branch`, the name is checked in all rigs before anything is created;
   with the default name (which needs the epic id) the check runs right after
   the epic is created, before any push, and the epic is closed if it fails.
   Branches are cut from each rig's default branch in its `.repo.git` and
   pushed to origin. If any rig fails, the branches already pushed are deleted.
   `add-rig <feature> <rig>` does the same for one more rig.
2. `gt project feature task <feature> <rig> "<title>" [-d desc] [--after id]...`
   creates the bead in the rig's database and records it. The bead description
   starts with a `project_feature: <epic-id>` line (same `key: value` convention
   as the other bead fields).
3. `gt project feature order <feature>` fills the `after` edges from the service
   map: tasks of rig A wait for all tasks of rig B when A calls B or consumes an
   event B publishes. Rigs that depend on each other get no order between them
   (a warning is printed), and so does an edge that would close a cycle with
   explicit `--after` edges. Explicit edges are kept when `order` is run again.
4. **Target resolution fails closed.** For a bead with the marker (or listed in
   a record), polecat spawn bases the worktree on `origin/<feature branch>`,
   `gt done` and `gt mq submit` target the feature branch, and `--target`,
   `--base-branch`, `--epic` and the `direct` merge strategy cannot point
   elsewhere. If the record is missing, the rig is not part of the feature, the
   feature is landed or the branch does not exist on origin, the command fails
   with an error instead of using the default branch. Beads without the marker
   behave exactly as before.
5. `gt project feature run <feature>` slings every ready task (bead open, every
   `after` task closed, rig in the project) through `executeSling`, the same
   path as batch sling. It is idempotent. `status <feature> [--json]` shows
   the branch per rig and, per task, the live bead status and whether it is
   ready, blocked, running or done.
6. The daemon heartbeat runs `gt project feature run <id> --if-started` for
   every open feature, at most once a minute per feature, in the background and
   behind the polecat pressure gate. `--if-started` makes the run a no-op until
   some task has left `open`, so the first dispatch stays an explicit decision.
   Errors are logged; the heartbeat never fails because of it.
7. A task counts as done when its bead is closed and no MR for it is open.
   `gt done` closes the bead when the polecat submits, before the refinery
   merges, so a closed task with an open MR shows as `merging` and releases
   nothing. If the merge queue cannot be read, dependents wait.
   A polecat on a feature task also gets read-only checkouts of the feature's
   other rigs at the feature branch, so it can read what sibling tasks merged.
8. `gt project feature land <feature> [--dry-run]` lands the feature across its
   rigs as a linked set (`internal/projectfeature/land.go`). Every pass is
   idempotent and driven by re-running the command.
   - **Preconditions**, all reported together, nothing changes if one fails:
     every task bead closed; no open or in-progress MR targets the feature
     branch in any rig; each rig's feature branch exists on origin and is ahead
     of its base. A rig with no commits ahead gets state `skipped`; if every rig
     is skipped, landing is an error.
   - **Conflict preflight**: each branch is test-merged into its current base
     with `git merge-tree --write-tree` in the rig's bare repo (no ref is
     touched). Any conflict stops before anything lands and names rigs and
     files.
   - **Landing order**: rigs whose tasks wait (`after`) for another rig's tasks
     land later; ties are alphabetical; rigs in a cycle stay alphabetical among
     themselves and a warning is printed. The order is stored in the record as
     `landing_order`.
   - **`direct`**: per rig, in order, a `--no-ff` merge in a temporary worktree
     of the bare repo, the rig's `merge_queue.test_command` if it has one, then
     a push of the base with `GT_INTEGRATION_LAND=1`. The merge commit names the
     feature id and landing order. A failure sets the rig and feature to `held`
     with the error, keeps what already landed and prints what landed and what
     did not. Re-running resumes at the first rig that has not landed.
   - **`pr`** (default): see "PR layer" below.
   - **Completion**: when every rig is landed or skipped, the epic bead is
     closed, the feature branch is deleted on origin and in the rig repo (best
     effort, warnings only), the state becomes `landed` and an incremental
     service map build runs. Landing a landed feature again is a no-op.
   - `--dry-run` prints the plan and the preflight result and changes nothing
     (it only refreshes remote-tracking refs). `status` shows state, PR URL and
     landed commit per rig, the landing order and what is blocking.
9. The daemon heartbeat runs `gt project feature land <id>` for features in
   state `landing`, at most once every 5 minutes per feature, in the background.
   It never starts a landing: only a human running `land` moves a feature from
   `open` to `landing`. `open` and `held` features are left alone; a held
   feature resumes when someone fixes the cause and re-runs `land`.

Each polecat writes to its own worktree and gets read-only checkouts of the
neighbouring repos; a contract change also runs the consumers' tests.

### PR layer

`internal/vcs` is the pull request layer the project landing coordinator calls
to land an integration branch, one PR per repo. It is separate from the
refinery's `PRProvider` (a merge-queue abstraction with no create or checks
operation, whose Go callers are not used in production; the refinery formula
calls `gh` directly).

- `vcs.Provider`: `CreatePR` (idempotent: an open head to base PR is returned),
  `FindPR`, `GetPR`, `Checks` (pending, passing, failing, none), `ReviewState`
  (approved, pending, rejected) and `Merge` (squash or merge, optional
  `ExpectHead`). A `PR` carries its base ref so callers verify the target.
- GitHub uses `gh ... --json` (`--match-head-commit` for the head guard).
- Azure DevOps uses `az repos pr ... --output json`. Organization, project and
  repository come from the git remote (`dev.azure.com`, `ssh.dev.azure.com`,
  legacy `visualstudio.com`). Reviewer votes of 10 or more mean approved, -10
  or less rejected. Completion is asynchronous, so `Merge` polls
  `az repos pr show` until the PR is completed, within a timeout, and reads the
  merge commit from `lastMergeCommit`.
- `vcs.ForRemote(url)` detects the provider; `vcs.New("github"|"azure")` picks
  one. Bitbucket returns an unsupported error.
- Project settings `merge_strategy` (pr by default), `vcs_provider` (detected
  from the remote when empty) and `require_review` (true by default) are read
  with `config.ProjectLandSettings`.
- `UpdatePRBody` replaces a PR description (`gh pr edit --body`,
  `az repos pr update --description`); the coordinator uses it to link sibling
  PRs.
- Commands use exec args, never a shell, and no credentials are read, passed or
  logged: auth is whatever `gh auth` or `az login` holds.

**Landing a feature with PRs** (`merge_strategy: pr`). `land` runs two stages,
both idempotent. The provider is resolved per rig from `vcs_provider` or the
rig's origin remote (`projectfeature.NewProvider`, replaced by a fake in tests).

1. *Open*: `CreatePR` for every rig without a PR (head is the feature branch,
   base is the rig's base, title `<feature title> (<rig> n of m)`, body naming
   the feature id, landing order and sibling repos). Once all exist, each PR
   body is updated with links to its siblings (a failure to do so is a
   warning). The feature becomes `landing`. A re-run reuses the recorded PRs.
2. *Advance*: read checks and, when `require_review`, the review state of every
   open PR. If any is rejected, failing, closed or targets another base, the
   feature is `held` with the reasons and nothing merges. If any is pending,
   the run reports and waits (the daemon re-runs it). Only when all are passing
   and approved do PRs merge, in landing order, with `ExpectHead` set to the
   head recorded at evaluation (`pr_head`); the PR, checks, review and the
   merge against the current base are re-read before each merge. A failed merge
   (`ErrHeadMoved`, `ErrNotMerged`, anything else) holds the feature and leaves
   the remaining PRs open. A PR merged outside gt (GetPR state merged) counts as
   landed. The merge method is `merge`, so each repo's history keeps a merge
   commit; the PR title and body carry the feature id.

## Review gate (lavish)

`lavish-axi` opens an agent's HTML page in the browser, the human annotates it
and presses a button, and `lavish-axi poll` returns what they sent. gt watches
this deterministically; no model is involved.

### Asking for a review

`gt review request <page.html> --bead <id> --kind plan|design|question|approval`

- Copies the page and the files next to it (up to 50 MB; more is refused) to
  `<town>/.runtime/reviews/<id>/`, so it survives polecat cleanup.
- Adds a small gt panel before `</body>` in the copy: Approve, Request changes
  and an answer box (only the answer box for `question`). The buttons call
  `window.lavish.queuePrompt(..., {tag: 'gt-review', data: {decision, answer}})`
  and then `window.lavish.sendQueuedPrompts()`.
- Opens a lavish session with `--no-open`, prints one link and returns at once.
  The agent never waits.
- `gt review list`, `show <id>`, `cancel <id>` (ends the lavish session) and
  `reopen <id>` (resumes a disconnected or ended review). `gt review watch`
  runs the watcher in a terminal for a town without a daemon.

### Records are the truth

`<town>/.runtime/reviews/<id>.json` holds the id, bead, kind, the page copy,
the link, the requester address, the state, the feedback list and times. States
are `open`, `approved`, `rejected`, `answered`, `ended` and `disconnected`.
Writes are atomic. The bead gets a mirror: the label `review:pending`,
`review:approved` or `review:rejected`, and a comment with the feedback. The
bead is found by its prefix through the town routes, so it can live in a rig,
project or town database. If the mirror fails, the record is still right.

### Towns stay separate

Every lavish call from gt sets `LAVISH_AXI_STATE_DIR=<town>/.runtime/lavish`,
`LAVISH_AXI_PORT`, `LAVISH_AXI_TELEMETRY=0` (turns telemetry off) and
`LAVISH_AXI_NO_OPEN=1`. The port is the Dolt port plus 1080 (3307 gives 4387,
3308 gives 4388), or the town setting `review_port`. gt also sets
`LAVISH_AXI_IDLE_TIMEOUT_MS` to one week: lavish stops an idle server after 30
minutes, and a review whose window was closed has no poll, so its link would
die while the reviewer is away.

Lavish prints links on the machine's Tailscale name over plain HTTP, which
browsers do not accept for `ts.net` names. The town setting
`review_public_url` (for example `https://gastown-test.tail616a3d.ts.net:8443`,
Tailscale Serve in front of lavish) replaces the base of every link gt prints.
The same host name is passed as `LAVISH_AXI_ALLOWED_HOSTS`, because lavish
rejects requests whose Host header it does not know.

### The daemon watches

Each heartbeat the daemon makes sure there is one child
`lavish-axi poll <copy> --owner gt-review` per open review (no shell, tracked
by the daemon). With no open review the tick does nothing. Poll is exclusive
and is not killed while it waits; when the daemon shuts down the polls are
stopped and undelivered feedback stays queued in lavish. After a crash, a poll
left over from the earlier daemon is displaced with `--takeover` when lavish
answers `LISTENER_ACTIVE`.

`lavish-axi poll` has no JSON mode. Its only machine output is TOON on stdout,
so gt reads that with a small parser, and a contract test drives a real session
(`internal/review/contract_test.go`, skipped when lavish-axi is not installed).

When poll returns:

| Poll result | Record | Then |
|---|---|---|
| gt panel Approve | `approved` | bead label and comment, `lavish-axi reply`, tell the requester |
| gt panel Request changes | `rejected` | same |
| gt panel answer on a `question` | `answered` | same |
| other feedback or annotations | stays `open`, feedback saved | reply, tell the requester, keep polling |
| artifact failure | stays `open`, failure saved | tell the requester, keep polling |
| Send & End | the decision above, else `ended`; the feedback is kept | tell the requester |
| `browser_disconnected` | `disconnected` | no poll and no reopen; `gt review reopen <id>` resumes |

The reply makes the browser stop showing Working. The requester is told by
`gt nudge` when their session is alive, otherwise by `gt mail` to the mayor, so
the outcome survives the agent's session.

### The gate

- Project setting `require_visual_review` (default false). When set,
  `gt project feature land` (direct and PR, on every pass) waits unless the
  epic or one of its tasks has an approved `approval` review and none is open
  or rejected. The message says which. A rejected review blocks.
- For each bead only the newest `approval` review counts, so a fixed page that
  was approved again is not blocked by the old rejection. A review that was
  cancelled or ended without a decision counts for nothing.
- The refinery never lists a merge request as ready while its source bead has
  an open or rejected `approval` review, whether or not the project requires
  one (such a review exists only if someone asked). The MR stays queued and the
  refinery log says why.
- If the review store cannot be read, both gates fail closed.
- `gt project feature status` prints a `Visual review:` line.

Known limits of the gate:

- Page scripts run in the same page as the gt panel, so a script in an agent's
  page could submit a decision. The gate guards against mistakes, not against a
  hostile agent. Use PR review on the platform where that matters.
- An approval is tied to code, not only to the page. `gt review request` with
  kind `approval` records what was reviewed: for the epic or a task of a
  feature, the tip of each rig's feature branch on origin; otherwise the git
  HEAD of the working directory, when it is a repo. When a recorded commit
  later differs from the current one, the approval stops counting in the
  feature gate and in the refinery merge check, and the message says the code
  changed since approval and to request a new review. If a recorded tip cannot
  be read (branch deleted, work tree removed, no network), the gate fails
  closed. A review with nothing recorded (not in a repo, no feature) behaves as
  before. The tip is read for the working directory of the person who asked, so
  it follows that directory, not the code the refinery merges.

## Tools policy

| Caller | Uses | Why |
|---|---|---|
| Agents | AXI tools (`az-axi`, `gh-axi`, `chrome-devtools-axi`, `lavish-axi`, `quota-axi`) | token-efficient output, next-step hints, no prompts |
| gt Go code | `az ... --output json`, `gh ... --json`, REST APIs | complete data, stable format, separate errors, testable |

AXI output is truncated, minimal and free to change (pre-1.0, TOON format), so
gt code never parses it. Exceptions where no stable layer exists (`quota-axi`,
`lavish-axi poll`) use their machine output mode, pinned versions and contract
tests.

Each town lists the AXI tools it needs in the `axi_tools` setting: tool names,
each optionally pinned as `name@version` (see the snippet under "Org =
town"). `contrib/fork-host/install-axi.sh` installs the pinned versions. Merging their
session-start context into `gt prime` is planned (phase 2b), not implemented yet.

`gt doctor` runs two checks:

- `org-env`: when `org_env` is set, it must resolve and yield at least one
  variable; otherwise a warning (agents would start without it).
- `axi-tools`: for each configured tool, it must be on `PATH` (with the org
  environment applied), `--version` must match the pin if there is one, and
  running it with no arguments (15 second timeout, in the town root, with the
  org environment) must exit 0. A failure reports the tool's first error line,
  which catches problems like az-axi's `SubscriptionNotFound` for a stale
  Azure subscription.

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
| 3 | Project brain: brief, service map, read-only neighbours | done |
| 4 | Multi-repo features: feature records, same-name branches, ordered dispatch, linked PRs and landing (direct and PR), GitHub and Azure DevOps PR providers | done |
| 5 | Review gate: `gt review`, daemon watcher, feature land gate, refinery approval gate | done (tested against real lavish-axi through its API; the page panel is not yet tried in a real browser) |
| 6 | Runtime routing by quota | planned |
| 7 | Planning sync with Azure Boards / GitHub | planned |
| 8 | AXI-style output for `gt` | planned |
