#!/usr/bin/env bash
# Build a reproducible multi-repo demo project ("shop") for testing a project layer.
# Layout: <DEMO_ROOT>/shop/{README.md,docs,code,data} plus <DEMO_ROOT>/remotes/<repo>.git
# Usage: make-demo.sh [--force]    (DEMO_ROOT defaults to $HOME/work/demo)
set -euo pipefail

ROOT="${DEMO_ROOT:-$HOME/work/demo}"
MARKER="$ROOT/.demo-fixture"
PROJECT="$ROOT/shop"
CODE="$PROJECT/code"
REMOTES="$ROOT/remotes"

if [[ "${1:-}" == "--force" ]]; then
  if [[ -f "$MARKER" ]]; then
    rm -rf -- "$ROOT"
  elif [[ -e "$ROOT" ]]; then
    echo "refusing to delete $ROOT: no .demo-fixture marker (not created by this script)" >&2
    exit 1
  fi
elif [[ -n "${1:-}" ]]; then
  echo "usage: $0 [--force]" >&2
  exit 2
fi

if [[ -d "$ROOT" && ! -f "$MARKER" && -n "$(ls -A "$ROOT")" ]]; then
  echo "refusing to use non-empty $ROOT without .demo-fixture marker" >&2
  exit 1
fi
mkdir -p "$ROOT"
echo "shop demo fixture, safe to delete with make-demo.sh --force" >"$MARKER"

# repo|owns|calls|publishes|consumes|database (comma separated lists, - for none)
REPOS='contracts|schemas||||-
api-gateway|edge routing|users-svc,catalog-svc,orders-svc|||-
users-svc|accounts||||users.db
catalog-svc|products||||catalog.db
orders-svc|orders|catalog-svc,billing-svc|order.placed||orders.db
billing-svc|payments||payment.captured|order.placed|billing.db
notify-svc|notifications|||order.placed,payment.captured|-
web|storefront ui|api-gateway|||-'

# repo|route|summary|payload (python dict literal)
ROUTES='api-gateway|/v1/users/{id}|Proxy to users-svc|{"id": "{id}", "via": "users-svc"}
api-gateway|/v1/products|Proxy to catalog-svc|{"items": [], "via": "catalog-svc"}
api-gateway|/v1/orders/{id}|Proxy to orders-svc|{"id": "{id}", "via": "orders-svc"}
users-svc|/users/{id}|Fetch a user|{"id": "{id}", "name": "Ada", "email": "ada@example.invalid"}
catalog-svc|/products|List products|{"items": [{"sku": "sku-1", "price_cents": 1999}]}
catalog-svc|/products/{sku}|Fetch a product|{"sku": "{sku}", "price_cents": 1999}
orders-svc|/orders|List orders|{"items": []}
orders-svc|/orders/{id}|Fetch an order|{"id": "{id}", "total_cents": 1999, "status": "placed"}
billing-svc|/payments/{id}|Fetch a payment|{"id": "{id}", "amount_cents": 1999, "status": "captured"}
notify-svc|/notifications|List sent notifications|{"items": []}
web|/|Storefront landing page|{"title": "Shop", "backend": "api-gateway"}'

tolist() { # "a,b" -> "a", "b"
  local out="" item
  [[ -z "$1" || "$1" == "-" ]] && return 0
  IFS=',' read -ra arr <<<"$1"
  for item in "${arr[@]}"; do out+="${out:+, }\"$item\""; done
  printf '%s' "$out"
}

git_demo() { # git_demo <repo-dir> <date-index> <message>
  local dir="$1" n="$2" msg="$3"
  local d="2025-01-0${n}T12:00:00+0000"
  git -C "$dir" add -A
  GIT_AUTHOR_DATE="$d" GIT_COMMITTER_DATE="$d" git -C "$dir" commit -q -m "$msg"
}

write_project() {
  mkdir -p "$PROJECT/docs" "$PROJECT/code" "$PROJECT/data"
  cat >"$PROJECT/README.md" <<'EOF'
# shop

Demo project: a small e-commerce system split into 8 repos under `code/`.

- `docs/`: architecture and decisions
- `code/`: one git repo per service (`contracts` is shared schemas)
- `data/`: scratch data for experiments (empty by default)
EOF
  cat >"$PROJECT/docs/architecture.md" <<'EOF'
# shop architecture

shop is a toy online store. Eight repos:

- contracts: shared JSON schemas (events, money type). No HTTP.
- api-gateway: single entry point, calls users-svc, catalog-svc, orders-svc.
- users-svc: accounts. catalog-svc: products.
- orders-svc: places orders, calls catalog-svc and billing-svc, publishes `order.placed`.
- billing-svc: consumes `order.placed`, publishes `payment.captured`.
- notify-svc: consumes `order.placed` and `payment.captured`.
- web: storefront, talks only to api-gateway.

Synchronous calls use HTTP+JSON. Asynchronous facts travel as events on the bus `shop.events`.
EOF
  cat >"$PROJECT/docs/decisions.md" <<'EOF'
# shop decisions

1. Money is integer cents. Never floats. Currency is carried next to the amount.
2. Events are JSON over a bus named `shop.events`; schemas live in `contracts`.
3. All services expose `/health` returning `{"status": "ok"}`.
EOF
}

# emit_repo <repo> <owns> <calls> <pubs> <cons> <db>
emit_repo() {
  local repo="$1" owns="$2" calls="$3" pubs="$4" cons="$5" db="$6"
  local dir="$CODE/$repo"
  [[ "$db" == "-" ]] && db=""
  mkdir -p "$dir/tests"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name "Demo Bot"
  git -C "$dir" config user.email "demo@example.invalid"

  # commit 1: manifest and readme
  cat >"$dir/service.toml" <<EOF
name = "$repo"
owns = "$owns"
calls = [$(tolist "$calls")]
publishes = [$(tolist "$pubs")]
consumes = [$(tolist "$cons")]
database = "$db"
EOF
  printf '# %s\n\nDemo service owning: %s.\n\nRun tests: `python3 -m unittest discover -s tests`\n' "$repo" "$owns" >"$dir/README.md"
  git_demo "$dir" 1 "Add manifest and README"

  # commit 2: code + tests (+ openapi)
  if [[ "$repo" == "contracts" ]]; then
    emit_contracts "$dir"
  else
    emit_service "$dir" "$repo" "$calls" "$pubs" "$cons"
  fi
  git_demo "$dir" 2 "Add implementation and tests"

  # commit 3: document dependencies
  {
    printf '\n## Dependencies\n\n'
    printf -- '- calls: %s\n' "${calls:-none}"
    printf -- '- publishes: %s\n' "${pubs:-none}"
    printf -- '- consumes: %s\n' "${cons:-none}"
    [[ "$repo" != "contracts" ]] && printf -- '- shared types: contracts\n'
  } >>"$dir/README.md"
  git_demo "$dir" 3 "Document dependencies in README"
}

emit_contracts() {
  local dir="$1"
  mkdir -p "$dir/schemas"
  cat >"$dir/schemas/money.json" <<'EOF'
{"title": "Money", "type": "object", "required": ["amount_cents", "currency"],
 "properties": {"amount_cents": {"type": "integer"}, "currency": {"type": "string"}}}
EOF
  cat >"$dir/schemas/order.placed.json" <<'EOF'
{"title": "order.placed", "type": "object", "required": ["order_id", "user_id", "total"],
 "properties": {"order_id": {"type": "string"}, "user_id": {"type": "string"}, "total": {"$ref": "money.json"}}}
EOF
  cat >"$dir/schemas/payment.captured.json" <<'EOF'
{"title": "payment.captured", "type": "object", "required": ["payment_id", "order_id", "amount"],
 "properties": {"payment_id": {"type": "string"}, "order_id": {"type": "string"}, "amount": {"$ref": "money.json"}}}
EOF
  cat >"$dir/money.py" <<'EOF'
"""Money helpers: always integer cents."""


def to_cents(text: str) -> int:
    whole, _, frac = text.partition(".")
    return int(whole) * 100 + int((frac + "00")[:2])


def format_cents(cents: int, currency: str = "USD") -> str:
    return f"{cents // 100}.{cents % 100:02d} {currency}"
EOF
  cat >"$dir/tests/test_contracts.py" <<'EOF'
import json
import pathlib
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
import money  # noqa: E402


class ContractsTest(unittest.TestCase):
    def test_schemas_are_valid_json_with_required(self):
        for path in (ROOT / "schemas").glob("*.json"):
            schema = json.loads(path.read_text())
            self.assertIn("required", schema, path.name)

    def test_money_roundtrip(self):
        self.assertEqual(money.to_cents("19.99"), 1999)
        self.assertEqual(money.format_cents(1999), "19.99 USD")
        self.assertIsInstance(money.to_cents("5"), int)


if __name__ == "__main__":
    unittest.main()
EOF
}

emit_service() {
  local dir="$1" repo="$2" calls="$3" pubs="$4" cons="$5"
  local routes_for first_route route summary payload r
  routes_for=$(grep "^$repo|" <<<"$ROUTES" || true)

  {
    printf '"""%s: tiny stdlib-only demo service."""\n' "$repo"
    printf 'import json\nimport re\nfrom http.server import BaseHTTPRequestHandler, HTTPServer\n\n'
    printf 'NAME = "%s"\nCALLS = [%s]\nPUBLISHES = [%s]\nCONSUMES = [%s]\n\n' \
      "$repo" "$(tolist "$calls")" "$(tolist "$pubs")" "$(tolist "$cons")"
    printf 'ROUTES = {\n    "/health": {"status": "ok"},\n'
    while IFS='|' read -r r route summary payload; do
      [[ -z "$r" ]] && continue
      printf '    "%s": %s,\n' "$route" "$payload"
    done <<<"$routes_for"
    printf '}\n'
    cat <<'EOF'


def handle(path):
    """Return (status, body) for a GET path."""
    for template, body in ROUTES.items():
        pattern = re.sub(r"\{(\w+)\}", r"(?P<\1>[^/]+)", template)
        match = re.fullmatch(pattern, path)
        if match:
            return 200, _fill(body, match.groupdict())
    return 404, {"error": "not found"}


def _fill(value, params):
    if isinstance(value, str):
        for key, val in params.items():
            value = value.replace("{" + key + "}", val)
        return value
    if isinstance(value, dict):
        return {k: _fill(v, params) for k, v in value.items()}
    if isinstance(value, list):
        return [_fill(v, params) for v in value]
    return value


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        status, body = handle(self.path)
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(data)


def main(port=8080):
    HTTPServer(("", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
EOF
  } >"$dir/app.py"

  # openapi
  {
    printf 'openapi: 3.0.3\ninfo:\n  title: %s\n  version: 0.1.0\npaths:\n' "$repo"
    printf '  /health:\n    get:\n      summary: Liveness check\n      responses:\n        "200":\n          description: ok\n'
    while IFS='|' read -r r route summary payload; do
      [[ -z "$r" ]] && continue
      printf '  %s:\n    get:\n      summary: %s\n      responses:\n        "200":\n          description: ok\n' "$route" "$summary"
    done <<<"$routes_for"
  } >"$dir/openapi.yaml"
  sed -i 's#^  /:#  "/":#' "$dir/openapi.yaml"

  # tests
  first_route=$(awk -F'|' -v r="$repo" '$1==r{print $2; exit}' <<<"$ROUTES" | sed 's/{[a-z]*}/42/g')
  cat >"$dir/tests/test_app.py" <<EOF
import pathlib
import sys
import tomllib
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
import app  # noqa: E402


class AppTest(unittest.TestCase):
    def test_health(self):
        self.assertEqual(app.handle("/health"), (200, {"status": "ok"}))

    def test_primary_route(self):
        status, body = app.handle("$first_route")
        self.assertEqual(status, 200)
        self.assertNotRegex(str(body), r"\{\w+\}")

    def test_unknown_route(self):
        self.assertEqual(app.handle("/nope")[0], 404)

    def test_manifest_matches_code(self):
        manifest = tomllib.loads((ROOT / "service.toml").read_text())
        self.assertEqual(manifest["name"], app.NAME)
        self.assertEqual(manifest["calls"], app.CALLS)
        self.assertEqual(manifest["publishes"], app.PUBLISHES)
        self.assertEqual(manifest["consumes"], app.CONSUMES)


if __name__ == "__main__":
    unittest.main()
EOF
}

setup_remote() {
  local repo="$1"
  local bare="$REMOTES/$repo.git"
  [[ -d "$bare" ]] || git init -q --bare -b main "$bare"
  if git -C "$CODE/$repo" remote get-url origin >/dev/null 2>&1; then
    git -C "$CODE/$repo" remote set-url origin "$bare"
  else
    git -C "$CODE/$repo" remote add origin "$bare"
  fi
  git -C "$CODE/$repo" push -q -u origin main
}

write_project
mkdir -p "$REMOTES"
while IFS='|' read -r repo owns calls pubs cons db; do
  if [[ -d "$CODE/$repo/.git" ]]; then
    echo "skip $repo (exists)"
  else
    emit_repo "$repo" "$owns" "$calls" "$pubs" "$cons" "$db"
  fi
  setup_remote "$repo"
done <<<"$REPOS"

echo
echo "root: $ROOT"
echo "project: $PROJECT"
echo "repos[8]{repo,calls,publishes,consumes}:"
while IFS='|' read -r repo _ calls pubs cons _; do
  calls=${calls//,/;} pubs=${pubs//,/;} cons=${cons//,/;}
  printf '  %s,%s,%s,%s\n' "$repo" "${calls:--}" "${pubs:--}" "${cons:--}"
done <<<"$REPOS"
