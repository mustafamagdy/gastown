# project-demo

Reproducible multi-repo fixture for testing a Gas Town "project layer".
It builds a toy project `shop` made of 8 git repos, each with a bare local remote.

## Usage

```bash
contrib/project-demo/make-demo.sh           # build under ${DEMO_ROOT:-$HOME/work/demo}
contrib/project-demo/make-demo.sh --force   # delete the demo root and rebuild
DEMO_ROOT=/tmp/demo contrib/project-demo/make-demo.sh
```

Re-running without `--force` is idempotent: existing repos are skipped (no new commits),
remotes are re-pointed and pushed. `--force` only deletes a root containing the
`.demo-fixture` marker the script wrote; otherwise it refuses.
Commit dates are fixed and author is `Demo Bot <demo@example.invalid>` (set per repo).

## Layout

```
<DEMO_ROOT>/
  .demo-fixture
  shop/{README.md,docs/,code/,data/}
  shop/code/<repo>/        working repos, branch main, origin -> remotes/<repo>.git
  remotes/<repo>.git       bare remotes with main pushed
```

Each repo has `README.md`, `service.toml` (name, owns, calls, publishes, consumes, database),
stdlib-only Python 3.12 code, `openapi.yaml` (HTTP services) and `tests/`
(`python3 -m unittest discover -s tests`). 3 commits per repo.

## Dependency graph

| repo | calls (HTTP) | publishes | consumes |
|------|--------------|-----------|----------|
| contracts | none (shared schemas, used by all) | none | none |
| api-gateway | users-svc, catalog-svc, orders-svc | none | none |
| users-svc | none | none | none |
| catalog-svc | none | none | none |
| orders-svc | catalog-svc, billing-svc | order.placed | none |
| billing-svc | none | payment.captured | order.placed |
| notify-svc | none | none | order.placed, payment.captured |
| web | api-gateway | none | none |
