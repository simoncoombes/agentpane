#!/bin/sh
# Scaffold a synthetic project to record agentpane against.
#
#   sh tools/make-demo-project.sh [dir]      (default: /tmp/demo-project)
#
# A recording of a real session puts a real repository on screen: file names in
# every activity line, a directory in the session list, your own prompt as the
# label on main's row. This builds a project that exists only to be read, so
# every string a subagent can put on screen — path, package name, command, TODO
# text — is one we chose. It prints the prompt to drive it with when it is done.
#
# It refuses to delete a directory it did not create.

set -eu

DIR="${1:-/tmp/demo-project}"
MARKER=".agentpane-demo"

die() { echo "error: $*" >&2; exit 1; }

case "$DIR" in
  ""|"/"|"$HOME"|"$HOME/") die "refusing to scaffold over $DIR" ;;
esac

if [ -e "$DIR" ]; then
  # Re-runs between takes are the normal case, so an earlier fixture is
  # replaced without ceremony. Anything else is someone's actual work: this
  # script has an rm -rf in it and a path typed one character wrong must not
  # reach it.
  [ -f "$DIR/$MARKER" ] || die "$DIR already exists and was not created by this script"
  rm -rf "$DIR"
fi

mkdir -p "$DIR/src/api" "$DIR/src/core" "$DIR/src/util" "$DIR/tests" "$DIR/config" "$DIR/scripts"
cd "$DIR"
cat > "$MARKER" <<'EOF'
Created by agentpane's tools/make-demo-project.sh. Nothing here is real.
Delete it whenever you like; re-running the script replaces it.
EOF

cat > README.md <<'EOF'
# widget-service

A small service that accepts widget orders, validates them against a catalog,
and emits events downstream. This tree is a fixture: it exists to be read, not
to run.

## Layout

- `src/api/` — HTTP surface: routing, request handlers, serialization
- `src/core/` — the engine: validation, pricing, the event emitter
- `src/util/` — formatting and time helpers shared by both
- `config/` — layered settings, defaults first
- `tests/` — one file per module
EOF

cat > requirements.txt <<'EOF'
flask==3.0.3
pydantic==2.7.1
redis==5.0.4
httpx==0.27.0
pytest==8.2.0
EOF

cat > config/defaults.yaml <<'EOF'
service:
  name: widget-service
  port: 8080
  workers: 4
cache:
  backend: redis
  ttl_seconds: 300
catalog:
  refresh_minutes: 15
  strict: true
EOF

cat > config/settings.yaml <<'EOF'
# Overrides layered on defaults.yaml. Anything absent falls through.
service:
  workers: 8
cache:
  ttl_seconds: 60
limits:
  max_line_items: 50
  max_body_bytes: 262144
EOF

cat > src/api/routes.py <<'EOF'
"""Route table. Every path the service answers on is registered here."""

import os

from src.api.handlers import (
    create_order,
    get_order,
    health,
    list_catalog,
    retry_order,
)

BASE_PATH = os.environ.get("WIDGET_BASE_PATH", "/v1")
MAX_BODY = int(os.environ.get("WIDGET_MAX_BODY", "262144"))

ROUTES = [
    ("GET", f"{BASE_PATH}/health", health),
    ("GET", f"{BASE_PATH}/catalog", list_catalog),
    ("POST", f"{BASE_PATH}/orders", create_order),
    ("GET", f"{BASE_PATH}/orders/<order_id>", get_order),
    ("POST", f"{BASE_PATH}/orders/<order_id>/retry", retry_order),
]


def register(app):
    """Bind every route onto the app. Called once at startup."""
    for method, path, handler in ROUTES:
        app.add_url_rule(path, handler.__name__, handler, methods=[method])
    return app


# TODO: versioned routes — /v2 needs its own table rather than a prefix swap.
# FIXME: MAX_BODY is read once at import, so a config reload never reaches it.
EOF

cat > src/api/handlers.py <<'EOF'
"""Request handlers. Each one validates, calls the engine, and serializes."""

import os

from src.core.engine import Engine
from src.core.cache import Cache
from src.util.format import as_json, problem

ENGINE = Engine()
CACHE = Cache(ttl=int(os.environ.get("WIDGET_CACHE_TTL", "300")))
STRICT = os.environ.get("WIDGET_STRICT_CATALOG", "1") == "1"


def health(_request):
    return as_json({"status": "ok", "engine": ENGINE.state()})


def list_catalog(_request):
    cached = CACHE.get("catalog")
    if cached is not None:
        return as_json(cached)
    catalog = ENGINE.catalog(strict=STRICT)
    CACHE.set("catalog", catalog)
    return as_json(catalog)


def create_order(request):
    body = request.get_json(silent=True)
    if body is None:
        return problem(400, "body must be JSON")
    errors = ENGINE.validate(body)
    if errors:
        return problem(422, "validation failed", details=errors)
    order = ENGINE.place(body)
    return as_json(order, status=201)


def get_order(_request, order_id):
    order = ENGINE.lookup(order_id)
    if order is None:
        return problem(404, "no such order")
    return as_json(order)


def retry_order(_request, order_id):
    # HACK: retry re-runs the whole pipeline rather than resuming it, which
    # double-charges anything that succeeded before the failure.
    order = ENGINE.lookup(order_id)
    if order is None:
        return problem(404, "no such order")
    return as_json(ENGINE.place(order["request"]))
EOF

cat > src/core/engine.py <<'EOF'
"""The engine: validation, pricing, placement, and the event emit."""

import os
import time

from src.util.format import money, slugify

MAX_LINE_ITEMS = int(os.environ.get("WIDGET_MAX_ITEMS", "50"))
PRICE_TABLE = {"small": 250, "medium": 700, "large": 1900}


class Engine:
    def __init__(self):
        self._orders = {}
        self._started = time.time()

    def state(self):
        return {"orders": len(self._orders), "uptime_s": int(time.time() - self._started)}

    def catalog(self, strict=True):
        items = [{"size": s, "price": money(p)} for s, p in sorted(PRICE_TABLE.items())]
        if strict:
            items = [i for i in items if i["price"] is not None]
        return {"items": items, "count": len(items)}

    def validate(self, body):
        errors = []
        items = body.get("items") or []
        if not items:
            errors.append("at least one line item is required")
        if len(items) > MAX_LINE_ITEMS:
            errors.append(f"at most {MAX_LINE_ITEMS} line items")
        for i, item in enumerate(items):
            if item.get("size") not in PRICE_TABLE:
                errors.append(f"items[{i}].size is not in the catalog")
            if int(item.get("quantity", 0)) < 1:
                errors.append(f"items[{i}].quantity must be positive")
        return errors

    def price(self, items):
        return sum(PRICE_TABLE[i["size"]] * int(i["quantity"]) for i in items)

    def place(self, body):
        order_id = slugify(body.get("reference") or f"order-{len(self._orders) + 1}")
        order = {
            "id": order_id,
            "total": money(self.price(body["items"])),
            "items": body["items"],
            "request": body,
        }
        self._orders[order_id] = order
        self.emit("order.placed", order_id)
        return order

    def lookup(self, order_id):
        return self._orders.get(order_id)

    def emit(self, topic, key):
        # TODO: emit to the real bus. This is a print because the fixture has
        # no downstream to talk to.
        print(f"[event] {topic} {key}")
EOF

cat > src/core/cache.py <<'EOF'
"""A tiny TTL cache. Redis in production, a dict here."""

import os
import time

BACKEND = os.environ.get("WIDGET_CACHE_BACKEND", "memory")


class Cache:
    def __init__(self, ttl=300):
        self.ttl = ttl
        self._values = {}

    def get(self, key):
        entry = self._values.get(key)
        if entry is None:
            return None
        value, stored_at = entry
        if time.time() - stored_at > self.ttl:
            del self._values[key]
            return None
        return value

    def set(self, key, value):
        self._values[key] = (value, time.time())

    def clear(self):
        self._values.clear()


# FIXME: no size bound — a hot key set with unique names grows without limit.
EOF

cat > src/util/format.py <<'EOF'
"""Serialization and formatting helpers shared by the API and the engine."""

import json
import re

_SLUG = re.compile(r"[^a-z0-9]+")


def as_json(payload, status=200):
    return (json.dumps(payload, sort_keys=True), status, {"content-type": "application/json"})


def problem(status, title, details=None):
    body = {"status": status, "title": title}
    if details:
        body["details"] = details
    return as_json(body, status=status)


def money(cents):
    if cents is None:
        return None
    return f"{cents // 100}.{cents % 100:02d}"


def slugify(text):
    return _SLUG.sub("-", str(text).lower()).strip("-")
EOF

cat > src/util/clock.py <<'EOF'
"""Time helpers. Isolated so tests can freeze the clock."""

import time

_FROZEN = None


def now():
    return _FROZEN if _FROZEN is not None else time.time()


def freeze(at):
    global _FROZEN
    _FROZEN = at


def unfreeze():
    global _FROZEN
    _FROZEN = None


# TODO: timezone handling — everything here is naive epoch seconds.
EOF

cat > tests/test_handlers.py <<'EOF'
"""Handler tests: status codes and the shape of each response body."""

from src.core.engine import Engine


def test_health_reports_ok():
    assert Engine().state()["orders"] == 0


def test_catalog_is_sorted():
    items = Engine().catalog()["items"]
    assert [i["size"] for i in items] == sorted(i["size"] for i in items)


def test_create_rejects_an_empty_body():
    assert Engine().validate({}) != []


def test_create_rejects_an_unknown_size():
    errors = Engine().validate({"items": [{"size": "enormous", "quantity": 1}]})
    assert any("catalog" in e for e in errors)
EOF

cat > tests/test_engine.py <<'EOF'
"""Engine tests: validation rules, pricing arithmetic, placement."""

from src.core.engine import Engine


def test_price_multiplies_quantity():
    e = Engine()
    assert e.price([{"size": "small", "quantity": 4}]) == 1000


def test_price_sums_line_items():
    e = Engine()
    total = e.price([{"size": "small", "quantity": 1}, {"size": "large", "quantity": 2}])
    assert total == 4050


def test_quantity_must_be_positive():
    errors = Engine().validate({"items": [{"size": "small", "quantity": 0}]})
    assert any("positive" in e for e in errors)


def test_place_assigns_an_id():
    order = Engine().place({"items": [{"size": "medium", "quantity": 1}]})
    assert order["id"]


def test_lookup_returns_none_for_a_stranger():
    assert Engine().lookup("nobody") is None
EOF

cat > tests/test_cache.py <<'EOF'
"""Cache tests: expiry, overwrite, clear."""

from src.core.cache import Cache


def test_get_returns_what_was_set():
    c = Cache(ttl=60)
    c.set("k", {"a": 1})
    assert c.get("k") == {"a": 1}


def test_missing_key_is_none():
    assert Cache().get("absent") is None


def test_expired_entry_is_dropped():
    c = Cache(ttl=0)
    c.set("k", "v")
    assert c.get("k") is None


def test_clear_empties_everything():
    c = Cache()
    c.set("k", "v")
    c.clear()
    assert c.get("k") is None
EOF

cat > tests/test_format.py <<'EOF'
"""Formatting tests: money rounding and slug rules."""

from src.util.format import money, slugify


def test_money_pads_the_minor_unit():
    assert money(705) == "7.05"


def test_money_passes_none_through():
    assert money(None) is None


def test_slugify_collapses_runs():
    assert slugify("Order  #42 -- Rush") == "order-42-rush"
EOF

cat > scripts/build.sh <<'EOF'
#!/bin/sh
# Fixture build script. Prints what it would do and exits.
set -eu
echo "checking formatting"
echo "running tests"
echo "building image widget-service:dev"
EOF
chmod +x scripts/build.sh

cat > scripts/seed.sh <<'EOF'
#!/bin/sh
# Fixture seeder. Writes nothing; the catalog is in-process.
set -eu
echo "seeding catalog: small medium large"
EOF
chmod +x scripts/seed.sh

files=$(find . -type f ! -name "$MARKER" | wc -l | tr -d ' ')
echo "created $DIR ($files files)"
cat <<'NEXT'

Start Claude Code there and paste this. The quoted names become the row slugs;
two waves means agents arrive against a tree that is already populated, while
the first wave lands in the list at its foot.

--------------------------------------------------------------------------
Survey this codebase using subagents, in two waves so I can watch them arrive.

Wave 1 — launch these four at once and wait for all of them to return:
  "file census"     count the files by extension and name the three largest
  "config surface"  list every environment variable the code reads, plus every key under config/
  "test survey"     count the tests and say in one line what each test file covers
  "readme scan"     read README.md and summarise this service in three sentences

Wave 2 — once wave 1 has landed, launch these three:
  "route map"       list every route and the handler it calls
  "todo sweep"      find every TODO, FIXME and HACK comment and group them by theme
  "dep audit"       list the dependencies and say what each one is used for

Give each subagent exactly the quoted name above as its description, and have
each one read at least four files before it answers. Read-only — no edits, no
writes. Then give me a five-line summary of what they found.
--------------------------------------------------------------------------

For the ⚑ NEEDS YOU state, add this line and run in DEFAULT permission mode:

  Also have "build check" run scripts/build.sh and report what it printed.

scripts/build.sh only echoes three lines, but Bash is not pre-approved in
default mode, so that agent blocks and the row goes bright and reversed.

Before recording: close your other Claude Code sessions. The session list on
the idle screen is the one place a real directory reaches the screen.
NEXT
