#!/bin/bash
# Fixture-driven test harness for install.sh.
#
# Every case builds a throwaway fixture HOME under a mktemp dir and points
# install.sh at it with --settings. The real ~/.claude is never read or
# written. Run with: /bin/bash tests/install_test.sh

set -u

REPO="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL="$REPO/install.sh"

if [ ! -f "$INSTALL" ]; then
  echo "FAIL: $INSTALL not found" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 required to run the tests" >&2
  exit 1
fi

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/agentpane-test.XXXXXX")"
# Normalize (no symlink resolution): macOS TMPDIR ends in "/" so mktemp can
# return a path with "//", which install.sh's abspath collapses. The harness
# must compare against the same normalized string.
ROOT="$(python3 -c 'import os, sys; print(os.path.abspath(sys.argv[1]))' "$ROOT")"
trap 'rm -rf "$ROOT"' EXIT

# Stub agentpane binary that answers "version" and always exits 0.
STUB_DIR="$ROOT/bin"
mkdir -p "$STUB_DIR"
STUB="$STUB_DIR/agentpane"
cat > "$STUB" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "version" ]; then echo "stub-1.0"; fi
exit 0
EOF
chmod +x "$STUB"

PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "FAIL: $1${2:+ -- $2}"; }

# run_install <extra args...>  -- always non-interactive stdin; captures output
# into $OUT and exit code into $RC.
OUT="$ROOT/out.txt"
RC=0
run_install() {
  /bin/bash "$INSTALL" "$@" > "$OUT" 2>&1 < /dev/null
  RC=$?
}

tree_hash() {
  find "$1" -type f -exec shasum -a 256 {} \; | sort | shasum -a 256
}

EVENTS_PY='EVENTS = ["PreToolUse","PostToolUse","UserPromptSubmit","Stop","SubagentStart","SubagentStop","SessionStart","SessionEnd","Notification","PermissionRequest","PreCompact","PostCompact"]'

# =============================================================== case 1
# Fresh install into plain settings: 12 events appended, other keys and the
# rest of the file byte-identical to a reconstruction that only adds hooks.
C="$ROOT/c01"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
data = {"model": "opus", "permissions": {"defaultMode": "auto", "allow": ["Bash(ls:*)"]}, "env": {"FOO": "bar"}}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "01 fresh install" "exit $RC: $(tail -3 "$OUT" | tr '\n' ' ')"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
path, stub = sys.argv[1], sys.argv[2]
actual = open(path).read()
data = {"model": "opus", "permissions": {"defaultMode": "auto", "allow": ["Bash(ls:*)"]}, "env": {"FOO": "bar"}}
data["hooks"] = {e: [{"matcher": "", "hooks": [{"type": "command", "command": stub + " hook", "timeout": 5}]}] for e in EVENTS}
expected = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
sys.exit(0 if actual == expected else 1)
PY
  then pass "01 fresh install: 12 events appended, other keys byte-identical"
  else fail "01 fresh install" "file content differs from expected"; fi
fi

# =============================================================== case 2
# Idempotent re-run: exit 0, no file change, says nothing to do.
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 0 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "nothing to do" "$OUT"; then
  pass "02 idempotent re-run: exit 0, file unchanged"
else
  fail "02 idempotent re-run" "rc=$RC changed=$([ "$BEFORE" != "$AFTER" ] && echo yes || echo no)"
fi
# No second backup should have been created by the no-op run.
NBACKUPS="$(ls "$C" | grep -c 'agentpane-backup' || true)"
if [ "$NBACKUPS" -eq 1 ]; then
  pass "02b idempotent re-run: no extra backup"
else
  fail "02b idempotent re-run" "expected 1 backup, found $NBACKUPS"
fi

# =============================================================== case 3
# Partial coverage: 4 events already covered with the right binary; only the
# missing 8 are added, no duplicates.
C="$ROOT/c03"; mkdir -p "$C"
S="$C/settings.json"
AP_STUB="$STUB" python3 - "$S" <<'PY'
import json, os, sys
cmd = os.environ["AP_STUB"] + " hook"
g = lambda: [{"matcher": "", "hooks": [{"type": "command", "command": cmd, "timeout": 5}]}]
data = {"hooks": {"PreToolUse": g(), "Stop": g(), "SessionStart": g(), "Notification": g()}}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "03 partial coverage" "exit $RC"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
cmd = sys.argv[2] + " hook"
ok = True
for e in EVENTS:
    groups = data["hooks"].get(e, [])
    mine = [g for g in groups if any(h.get("command") == cmd for h in g.get("hooks", []))]
    if len(mine) != 1:
        ok = False
sys.exit(0 if ok else 1)
PY
  then pass "03 partial coverage: exactly one agentpane group per event, no duplicates"
  else fail "03 partial coverage" "coverage wrong or duplicated"; fi
fi

# =============================================================== case 4
# Binary-path update in place: all 12 point at an old path; position of the
# agentpane group is preserved (a foreign group sits after it on PostToolUse).
C="$ROOT/c04"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
cmd = "/old/prefix/agentpane hook"
EVENTS = ["PreToolUse","PostToolUse","UserPromptSubmit","Stop","SubagentStart","SubagentStop","SessionStart","SessionEnd","Notification","PermissionRequest","PreCompact","PostCompact"]
hooks = {}
for e in EVENTS:
    hooks[e] = [{"matcher": "", "hooks": [{"type": "command", "command": cmd, "timeout": 5}]}]
hooks["PostToolUse"].append({"matcher": "Bash", "hooks": [{"type": "command", "command": "some-logger.sh", "timeout": 5}]})
open(sys.argv[1], "w").write(json.dumps({"hooks": hooks}, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "04 binary-path update" "exit $RC"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
cmd = sys.argv[2] + " hook"
ok = True
for e in EVENTS:
    groups = data["hooks"][e]
    if groups[0]["hooks"][0]["command"] != cmd:
        ok = False
    if any("agentpane" in (h.get("command") or "") and "/old/" in h.get("command") for g in groups for h in g.get("hooks", [])):
        ok = False
post = data["hooks"]["PostToolUse"]
if len(post) != 2 or post[1]["hooks"][0]["command"] != "some-logger.sh":
    ok = False
sys.exit(0 if ok else 1)
PY
  then pass "04 binary-path update: path rewritten in place, order preserved"
  else fail "04 binary-path update" "path not updated in place"; fi
  if grep -q "update agentpane binary path" "$OUT"; then
    pass "04b binary-path update: plan announced the update"
  else
    fail "04b binary-path update" "no update plan line in output"
  fi
fi

# =============================================================== case 5
# Symlinked settings: warning fires, write lands in the target, the symlink
# survives, and the backup sits beside the target.
C="$ROOT/c05"; mkdir -p "$C/home/.claude" "$C/fleet"
TARGET="$C/fleet/settings.json"
LINK="$C/home/.claude/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$TARGET"
ln -s "$TARGET" "$LINK"
run_install --settings "$LINK" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "05 symlinked settings" "exit $RC"
else
  ok=1
  # The warning names the fully resolved target (realpath), which on macOS may
  # differ from $TARGET by the /private prefix on /var and /tmp.
  TARGET_REAL="$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "$TARGET")"
  grep -qi "symlink" "$OUT" || ok=0
  grep -q "$TARGET_REAL" "$OUT" || ok=0
  grep -qi "overwritten\|overwrite" "$OUT" || ok=0
  [ -L "$LINK" ] || ok=0
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if len(d.get("hooks",{}))==12 else 1)' "$TARGET" || ok=0
  ls "$C/fleet"/settings.json.agentpane-backup-* >/dev/null 2>&1 || ok=0
  if ls "$C/home/.claude"/*agentpane-backup* >/dev/null 2>&1; then ok=0; fi
  if [ "$ok" -eq 1 ]; then
    pass "05 symlinked settings: warned, wrote target, backup beside target, link intact"
  else
    fail "05 symlinked settings" "see $OUT"
  fi
fi

# =============================================================== case 6
# Competing visualizer: it2-pane-hook.sh on SubagentStart plus AGENT_PANE_FOO
# env var. Strong warning fires, existing entry untouched, agentpane appended
# after it.
C="$ROOT/c06"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
data = {
  "env": {"AGENT_PANE_FOO": "1", "OTHER": "x"},
  "hooks": {
    "SubagentStart": [{"matcher": "", "hooks": [{"type": "command", "command": "/Users/x/.claude/it2-pane-hook.sh start", "timeout": 10}]}],
    "SubagentStop":  [{"matcher": "", "hooks": [{"type": "command", "command": "/Users/x/.claude/it2-pane-hook.sh stop",  "timeout": 10}]}]
  }
}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "06 competing visualizer" "exit $RC"
else
  ok=1
  grep -q "competing subagent visualizer on SubagentStart: /Users/x/.claude/it2-pane-hook.sh start" "$OUT" || ok=0
  grep -q "doubles panes" "$OUT" || ok=0
  grep -q "AGENT_PANE_FOO" "$OUT" || ok=0
  python3 - "$S" "$STUB" <<'PY' || ok=0
import json, sys
data = json.load(open(sys.argv[1]))
cmd = sys.argv[2] + " hook"
ss = data["hooks"]["SubagentStart"]
ok = (len(ss) == 2
      and ss[0]["hooks"][0]["command"] == "/Users/x/.claude/it2-pane-hook.sh start"
      and ss[0]["hooks"][0]["timeout"] == 10
      and ss[1]["hooks"][0]["command"] == cmd
      and data["env"] == {"AGENT_PANE_FOO": "1", "OTHER": "x"})
sys.exit(0 if ok else 1)
PY
  if [ "$ok" -eq 1 ]; then
    pass "06 competing visualizer: strong warning, entry untouched, agentpane appended after"
  else
    fail "06 competing visualizer" "see $OUT"
  fi
fi

# =============================================================== case 7
# Notification alerting hook: mild warning about double alerts.
C="$ROOT/c07"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
data = {"hooks": {"Notification": [{"matcher": "", "hooks": [{"type": "command", "command": "osascript -e 'display notification \"claude\"'", "timeout": 5}]}]}}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -eq 0 ] && grep -q "existing alerting hook on Notification" "$OUT" \
   && grep -q "double alerts" "$OUT" && grep -q -- "--no-bell" "$OUT"; then
  pass "07 alerting hook: mild double-alert warning with --no-bell pointer"
else
  fail "07 alerting hook" "rc=$RC, warning missing"
fi

# =============================================================== case 8
# statusLine key: informational line about --oneline.
C="$ROOT/c08"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "statusLine": {\n    "type": "command",\n    "command": "my-status.sh"\n  }\n}\n' > "$S"
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -eq 0 ] && grep -q -- "--oneline" "$OUT" && grep -q "statusLine" "$OUT"; then
  pass "08 statusLine: informational --oneline line"
else
  fail "08 statusLine" "rc=$RC, info line missing"
fi

# =============================================================== case 9
# Malformed JSON: abort with exit 2, file untouched, no backup.
C="$ROOT/c09"; mkdir -p "$C"
S="$C/settings.json"
printf '{ this is not json\n' > "$S"
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
NBACKUPS="$(ls "$C" | grep -c 'agentpane-backup' || true)"
if [ "$RC" -eq 2 ] && [ "$BEFORE" = "$AFTER" ] && [ "$NBACKUPS" -eq 0 ] && grep -qi "not valid JSON" "$OUT"; then
  pass "09 malformed JSON: exit 2, file untouched, no backup"
else
  fail "09 malformed JSON" "rc=$RC (want 2)"
fi

# =============================================================== case 10
# Missing settings file: created on --yes, containing only the hooks block.
C="$ROOT/c10"; mkdir -p "$C"
S="$C/home/.claude/settings.json"   # parent dirs do not exist yet
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ] || [ ! -f "$S" ]; then
  fail "10 missing settings" "rc=$RC, file created=$([ -f "$S" ] && echo yes || echo no)"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
cmd = sys.argv[2] + " hook"
ok = (list(data.keys()) == ["hooks"]
      and sorted(data["hooks"].keys()) == sorted(EVENTS)
      and all(data["hooks"][e] == [{"matcher": "", "hooks": [{"type": "command", "command": cmd, "timeout": 5}]}] for e in EVENTS))
sys.exit(0 if ok else 1)
PY
  then pass "10 missing settings: created with only the hooks block"
  else fail "10 missing settings" "created file has wrong shape"; fi
fi

# =============================================================== case 11
# --dry-run writes nothing anywhere: whole fixture tree checksummed.
C="$ROOT/c11"; mkdir -p "$C/home/.claude"
S="$C/home/.claude/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
H_BEFORE="$(tree_hash "$C")"
run_install --settings "$S" --binary "$STUB" --dry-run
H_AFTER="$(tree_hash "$C")"
if [ "$RC" -eq 0 ] && [ "$H_BEFORE" = "$H_AFTER" ] && grep -q '^+++' "$OUT" && grep -q "nothing was written" "$OUT"; then
  pass "11 dry-run: exit 0, unified diff shown, fixture tree unchanged"
else
  fail "11 dry-run" "rc=$RC treechanged=$([ "$H_BEFORE" != "$H_AFTER" ] && echo yes || echo no)"
fi

# =============================================================== case 12
# --uninstall removes exactly the agentpane entries (any binary path),
# prunes emptied groups/events, keeps the hooks key, preserves everything
# else byte-for-byte.
C="$ROOT/c12"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
ap = lambda p: {"matcher": "", "hooks": [{"type": "command", "command": p + "/agentpane hook", "timeout": 5}]}
data = {
  "model": "opus",
  "env": {"KEEP": "me"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "danger-guard.sh", "timeout": 5}]},
      ap("/one")
    ],
    "SubagentStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "it2-pane-hook.sh start", "timeout": 10}]},
      ap("/two")
    ],
    "Stop": [ap("/three")],
    "Notification": [
      {"matcher": "", "hooks": [
        {"type": "command", "command": "/four/agentpane hook", "timeout": 5},
        {"type": "command", "command": "osascript -e beep", "timeout": 5}
      ]}
    ]
  }
}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --uninstall --yes
if [ "$RC" -ne 0 ]; then
  fail "12 uninstall" "exit $RC"
else
  if python3 - "$S" <<'PY'
import json, sys
actual = open(sys.argv[1]).read()
expected_data = {
  "model": "opus",
  "env": {"KEEP": "me"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "danger-guard.sh", "timeout": 5}]}
    ],
    "SubagentStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "it2-pane-hook.sh start", "timeout": 10}]}
    ],
    "Notification": [
      {"matcher": "", "hooks": [
        {"type": "command", "command": "osascript -e beep", "timeout": 5}
      ]}
    ]
  }
}
expected = json.dumps(expected_data, indent=2, ensure_ascii=False) + "\n"
sys.exit(0 if actual == expected else 1)
PY
  then pass "12 uninstall: agentpane entries removed, everything else byte-for-byte"
  else fail "12 uninstall" "surviving content differs from expected"; fi
  if ls "$C"/settings.json.agentpane-backup-* >/dev/null 2>&1; then
    pass "12b uninstall: backup created"
  else
    fail "12b uninstall" "no backup"
  fi
fi

# =============================================================== case 13
# Uninstall with no agentpane entries: exit 0, nothing to do, no write.
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --uninstall --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 0 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "nothing to do" "$OUT"; then
  pass "13 uninstall idempotent: exit 0, file unchanged"
else
  fail "13 uninstall idempotent" "rc=$RC"
fi

# =============================================================== case 14
# Non-tty without --yes: refuses to write, exit 1, file unchanged.
C="$ROOT/c14"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB"
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 1 ] && [ "$BEFORE" = "$AFTER" ] && grep -q -- "--yes" "$OUT"; then
  pass "14 non-tty without --yes: refused with exit 1, file unchanged"
else
  fail "14 non-tty without --yes" "rc=$RC (want 1)"
fi

# =============================================================== case 15
# --print-snippet: emits only valid JSON with 12 events, no writes.
C="$ROOT/c15"; mkdir -p "$C"
run_install --binary "$STUB" --print-snippet
if [ "$RC" -eq 0 ] && python3 - "$OUT" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.loads(open(sys.argv[1]).read())
cmd = sys.argv[2] + " hook"
ok = (list(data.keys()) == ["hooks"]
      and sorted(data["hooks"].keys()) == sorted(EVENTS)
      and all(data["hooks"][e][0]["hooks"][0]["command"] == cmd for e in EVENTS))
sys.exit(0 if ok else 1)
PY
then
  pass "15 print-snippet: pure JSON, 12 events, absolute command"
else
  fail "15 print-snippet" "rc=$RC or output not the pure snippet"
fi

# =============================================================== case 16
# Non-list event value: abort with exit 2, file untouched, no backup. The
# malformed-but-present hook value must never be silently replaced.
C="$ROOT/c16"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "hooks": {\n    "Stop": "my-precious-hook.sh"\n  }\n}\n' > "$S"
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
NBACKUPS="$(ls "$C" | grep -c 'agentpane-backup' || true)"
if [ "$RC" -eq 2 ] && [ "$BEFORE" = "$AFTER" ] && [ "$NBACKUPS" -eq 0 ] && grep -q "not a list" "$OUT"; then
  pass "16 non-list event value: exit 2, file untouched, no backup"
else
  fail "16 non-list event value" "rc=$RC (want 2)"
fi

# =============================================================== case 17
# Foreign hook that merely mentions an agentpane path is neither rewritten
# by install nor removed by uninstall.
C="$ROOT/c17"; mkdir -p "$C"
S="$C/settings.json"
FOREIGN="notify.sh --after /Users/x/go/bin/agentpane hook --sound pop"
AP_FOREIGN="$FOREIGN" python3 - "$S" <<'PY'
import json, os, sys
data = {"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": os.environ["AP_FOREIGN"], "timeout": 5}]}]}}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
ok=1
[ "$RC" -eq 0 ] || ok=0
grep -q "update agentpane binary path" "$OUT" && ok=0
AP_FOREIGN="$FOREIGN" python3 - "$S" "$STUB" <<'PY' || ok=0
import json, os, sys
data = json.load(open(sys.argv[1]))
stop = data["hooks"]["Stop"]
ok = (len(stop) == 2
      and stop[0]["hooks"][0]["command"] == os.environ["AP_FOREIGN"]
      and stop[1]["hooks"][0]["command"] == sys.argv[2] + " hook")
sys.exit(0 if ok else 1)
PY
if [ "$ok" -eq 1 ]; then
  pass "17 foreign command mentioning agentpane path: untouched, agentpane appended after"
else
  fail "17 foreign command mentioning agentpane path" "rc=$RC, see $OUT"
fi
run_install --settings "$S" --uninstall --yes
ok=1
[ "$RC" -eq 0 ] || ok=0
AP_FOREIGN="$FOREIGN" python3 - "$S" <<'PY' || ok=0
import json, os, sys
data = json.load(open(sys.argv[1]))
stop = data["hooks"]["Stop"]
cmds = [h["command"] for g in stop for h in g["hooks"]]
sys.exit(0 if cmds == [os.environ["AP_FOREIGN"]] else 1)
PY
if [ "$ok" -eq 1 ]; then
  pass "17b uninstall: foreign command mentioning agentpane path survives"
else
  fail "17b uninstall" "foreign command removed or agentpane left behind"
fi

# =============================================================== case 18
# Binary not named "agentpane" (breaks re-run/uninstall recognition): refused
# with exit 2, nothing written.
C="$ROOT/c18"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
cp "$STUB" "$STUB_DIR/agentpane2"
chmod +x "$STUB_DIR/agentpane2"
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB_DIR/agentpane2" --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 2 ] && [ "$BEFORE" = "$AFTER" ] && grep -q 'not named "agentpane"' "$OUT"; then
  pass "18 non-agentpane basename: refused with exit 2, file untouched"
else
  fail "18 non-agentpane basename" "rc=$RC (want 2)"
fi

# =============================================================== case 19
# Binary-path update preserves user-added trailing arguments, and the result
# is idempotent on re-run.
C="$ROOT/c19"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
cmd = "/old/prefix/agentpane hook --socket /tmp/ap.sock"
EVENTS = ["PreToolUse","PostToolUse","UserPromptSubmit","Stop","SubagentStart","SubagentStop","SessionStart","SessionEnd","Notification","PermissionRequest","PreCompact","PostCompact"]
hooks = {e: [{"matcher": "", "hooks": [{"type": "command", "command": cmd, "timeout": 5}]}] for e in EVENTS}
open(sys.argv[1], "w").write(json.dumps({"hooks": hooks}, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "19 args preserved on update" "exit $RC"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
want = sys.argv[2] + " hook --socket /tmp/ap.sock"
ok = all(data["hooks"][e][0]["hooks"][0]["command"] == want and len(data["hooks"][e]) == 1 for e in EVENTS)
sys.exit(0 if ok else 1)
PY
  then pass "19 binary-path update preserves trailing arguments"
  else fail "19 args preserved on update" "arguments dropped or duplicated"; fi
fi
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 0 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "nothing to do" "$OUT"; then
  pass "19b re-run with preserved arguments: nothing to do"
else
  fail "19b re-run with preserved arguments" "rc=$RC"
fi

# =============================================================== case 20
# Stale old-path agentpane group next to a current one: the stale entry is
# fixed up and the exact duplicate pruned, leaving one agentpane group.
C="$ROOT/c20"; mkdir -p "$C"
S="$C/settings.json"
AP_STUB="$STUB" python3 - "$S" <<'PY'
import json, os, sys
cur = os.environ["AP_STUB"] + " hook"
g = lambda c: {"matcher": "", "hooks": [{"type": "command", "command": c, "timeout": 5}]}
EVENTS = ["PreToolUse","PostToolUse","UserPromptSubmit","Stop","SubagentStart","SubagentStop","SessionStart","SessionEnd","Notification","PermissionRequest","PreCompact","PostCompact"]
hooks = {e: [g(cur)] for e in EVENTS}
hooks["Stop"] = [g("/old/dead/agentpane hook"), g(cur)]
open(sys.argv[1], "w").write(json.dumps({"hooks": hooks}, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "20 stale group beside current" "exit $RC"
else
  if python3 - "$S" "$STUB" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
stop = data["hooks"]["Stop"]
cmds = [h["command"] for g in stop for h in g["hooks"]]
sys.exit(0 if cmds == [sys.argv[2] + " hook"] else 1)
PY
  then pass "20 stale group beside current: fixed and deduplicated to one entry"
  else fail "20 stale group beside current" "stale or duplicate entry survives"; fi
fi

# =============================================================== case 21
# Duplicate JSON keys: abort with exit 2, file untouched (a rewrite would
# silently drop the first value).
C="$ROOT/c21"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus",\n  "model": "sonnet"\n}\n' > "$S"
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 2 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "duplicate key" "$OUT"; then
  pass "21 duplicate JSON keys: exit 2, file untouched"
else
  fail "21 duplicate JSON keys" "rc=$RC (want 2)"
fi

# =============================================================== case 22
# 4-space-indented file keeps its indent unit; no whole-file reformat.
C="$ROOT/c22"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
data = {"model": "opus", "env": {"FOO": "bar"}}
open(sys.argv[1], "w").write(json.dumps(data, indent=4, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "22 indent preserved" "exit $RC"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
actual = open(sys.argv[1]).read()
data = {"model": "opus", "env": {"FOO": "bar"}}
data["hooks"] = {e: [{"matcher": "", "hooks": [{"type": "command", "command": sys.argv[2] + " hook", "timeout": 5}]}] for e in EVENTS}
expected = json.dumps(data, indent=4, ensure_ascii=False) + "\n"
sys.exit(0 if actual == expected else 1)
PY
  then pass "22 indent preserved: 4-space file stays 4-space"
  else fail "22 indent preserved" "file was reformatted"; fi
fi

# =============================================================== case 23
# Write-phase failure (read-only directory): [error] line and exit 2, no
# traceback, settings intact.
C="$ROOT/c23"; mkdir -p "$C/ro"
S="$C/ro/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
BEFORE="$(shasum -a 256 "$S")"
chmod 555 "$C/ro"
run_install --settings "$S" --binary "$STUB" --yes
chmod 755 "$C/ro"
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 2 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "failed to write" "$OUT" && ! grep -q "Traceback" "$OUT"; then
  pass "23 write failure: [error] + exit 2, no traceback, file intact"
else
  fail "23 write failure" "rc=$RC (want 2)"
fi

# =============================================================== case 24
# --autopane fresh add: 12 hook events plus ONE extra SessionStart group
# with matcher "startup|resume" running "<binary> autopane".
C="$ROOT/c24"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
run_install --settings "$S" --binary "$STUB" --autopane --yes
if [ "$RC" -ne 0 ]; then
  fail "24 autopane fresh add" "exit $RC: $(tail -3 "$OUT" | tr '\n' ' ')"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
hook_cmd = sys.argv[2] + " hook"
auto_cmd = sys.argv[2] + " autopane"
ok = True
for e in EVENTS:
    groups = data["hooks"].get(e, [])
    mine = [g for g in groups if any(h.get("command") == hook_cmd for h in g.get("hooks", []))]
    if len(mine) != 1:
        ok = False
ss = data["hooks"]["SessionStart"]
auto = [g for g in ss if any(h.get("command") == auto_cmd for h in g.get("hooks", []))]
if len(auto) != 1:
    ok = False
elif auto[0] != {"matcher": "startup|resume", "hooks": [{"type": "command", "command": auto_cmd, "timeout": 5}]}:
    ok = False
elif len(ss) != 2 or ss[1] != auto[0]:
    ok = False  # the autopane group is appended AFTER the hook group
sys.exit(0 if ok else 1)
PY
  then pass "24 autopane fresh add: 12 hook events + one startup|resume autopane group"
  else fail "24 autopane fresh add" "file shape wrong"; fi
fi

# =============================================================== case 24b
# --autopane idempotent re-run: exit 0, no change, nothing to do.
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --autopane --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 0 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "nothing to do" "$OUT"; then
  pass "24b autopane idempotent re-run: exit 0, file unchanged"
else
  fail "24b autopane idempotent re-run" "rc=$RC changed=$([ "$BEFORE" != "$AFTER" ] && echo yes || echo no)"
fi

# =============================================================== case 24c
# Plain re-run WITHOUT --autopane leaves the existing autopane entry alone
# and reports nothing to do.
BEFORE="$(shasum -a 256 "$S")"
run_install --settings "$S" --binary "$STUB" --yes
AFTER="$(shasum -a 256 "$S")"
if [ "$RC" -eq 0 ] && [ "$BEFORE" = "$AFTER" ] && grep -q "nothing to do" "$OUT"; then
  pass "24c plain re-run keeps the autopane entry, nothing to do"
else
  fail "24c plain re-run with autopane present" "rc=$RC"
fi

# =============================================================== case 25
# Round-trip: plain --uninstall removes BOTH the hook entries and the
# autopane entry; the file returns to its pre-install content.
run_install --settings "$S" --uninstall --yes
if [ "$RC" -ne 0 ]; then
  fail "25 round-trip uninstall" "exit $RC"
else
  if python3 - "$S" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
sys.exit(0 if data == {"model": "opus", "hooks": {}} else 1)
PY
  then pass "25 round-trip uninstall: hook + autopane entries all removed"
  else fail "25 round-trip uninstall" "agentpane entries survive: $(cat "$S")"; fi
fi

# =============================================================== case 26
# --uninstall --autopane removes ONLY the autopane entry; the 12 hook
# entries survive untouched.
C="$ROOT/c26"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
run_install --settings "$S" --binary "$STUB" --autopane --yes
run_install --settings "$S" --uninstall --autopane --yes
if [ "$RC" -ne 0 ]; then
  fail "26 uninstall --autopane" "exit $RC"
else
  if python3 - "$S" "$STUB" <<PY
import json, sys
$EVENTS_PY
data = json.load(open(sys.argv[1]))
hook_cmd = sys.argv[2] + " hook"
auto_cmd = sys.argv[2] + " autopane"
cmds = [h.get("command") for groups in data.get("hooks", {}).values()
        for g in groups for h in g.get("hooks", [])]
ok = (auto_cmd not in cmds
      and all(any(h.get("command") == hook_cmd for h in g.get("hooks", []))
              for e in EVENTS for g in data["hooks"][e]))
sys.exit(0 if ok else 1)
PY
  then pass "26 uninstall --autopane: only the autopane entry removed, hooks intact"
  else fail "26 uninstall --autopane" "hooks disturbed or autopane survives"; fi
fi
run_install --settings "$S" --uninstall --autopane --yes
if [ "$RC" -eq 0 ] && grep -q "nothing to do" "$OUT"; then
  pass "26b uninstall --autopane idempotent: nothing to do"
else
  fail "26b uninstall --autopane idempotent" "rc=$RC"
fi

# =============================================================== case 27
# --autopane --dry-run writes nothing anywhere (whole tree checksummed) and
# the plan names the autopane group.
C="$ROOT/c27"; mkdir -p "$C"
S="$C/settings.json"
printf '{\n  "model": "opus"\n}\n' > "$S"
H_BEFORE="$(tree_hash "$C")"
run_install --settings "$S" --binary "$STUB" --autopane --dry-run
H_AFTER="$(tree_hash "$C")"
if [ "$RC" -eq 0 ] && [ "$H_BEFORE" = "$H_AFTER" ] \
   && grep -q "startup|resume" "$OUT" && grep -q "nothing was written" "$OUT"; then
  pass "27 autopane dry-run: plan shows the startup|resume group, tree unchanged"
else
  fail "27 autopane dry-run" "rc=$RC treechanged=$([ "$H_BEFORE" != "$H_AFTER" ] && echo yes || echo no)"
fi

# =============================================================== case 28
# Autopane binary path is rebound in place on re-run (stale path, args kept),
# without --autopane on the command line.
C="$ROOT/c28"; mkdir -p "$C"
S="$C/settings.json"
python3 - "$S" <<'PY'
import json, sys
EVENTS = ["PreToolUse","PostToolUse","UserPromptSubmit","Stop","SubagentStart","SubagentStop","SessionStart","SessionEnd","Notification","PermissionRequest","PreCompact","PostCompact"]
hooks = {e: [{"matcher": "", "hooks": [{"type": "command", "command": "/old/prefix/agentpane hook", "timeout": 5}]}] for e in EVENTS}
hooks["SessionStart"].append({"matcher": "startup|resume", "hooks": [{"type": "command", "command": "/old/prefix/agentpane autopane --width narrow", "timeout": 5}]})
open(sys.argv[1], "w").write(json.dumps({"hooks": hooks}, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --yes
if [ "$RC" -ne 0 ]; then
  fail "28 autopane path rebind" "exit $RC"
else
  if python3 - "$S" "$STUB" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
ss = data["hooks"]["SessionStart"]
want = sys.argv[2] + " autopane --width narrow"
ok = (len(ss) == 2
      and ss[1]["matcher"] == "startup|resume"
      and ss[1]["hooks"][0]["command"] == want
      and ss[0]["hooks"][0]["command"] == sys.argv[2] + " hook")
sys.exit(0 if ok else 1)
PY
  then pass "28 autopane path rebind: path updated in place, args and matcher kept"
  else fail "28 autopane path rebind" "autopane entry not rebound: $(cat "$S")"; fi
fi

# =============================================================== case 29
# A foreign command merely mentioning an agentpane autopane path is neither
# rewritten nor removed (install and uninstall).
C="$ROOT/c29"; mkdir -p "$C"
S="$C/settings.json"
FOREIGN29="notify.sh --after /Users/x/go/bin/agentpane autopane --sound pop"
AP_FOREIGN="$FOREIGN29" python3 - "$S" <<'PY'
import json, os, sys
data = {"hooks": {"SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": os.environ["AP_FOREIGN"], "timeout": 5}]}]}}
open(sys.argv[1], "w").write(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
run_install --settings "$S" --binary "$STUB" --autopane --yes
ok=1
[ "$RC" -eq 0 ] || ok=0
AP_FOREIGN="$FOREIGN29" python3 - "$S" "$STUB" <<'PY' || ok=0
import json, os, sys
data = json.load(open(sys.argv[1]))
ss = data["hooks"]["SessionStart"]
cmds = [h["command"] for g in ss for h in g.get("hooks", [])]
ok = (os.environ["AP_FOREIGN"] in cmds and (sys.argv[2] + " autopane") in cmds)
sys.exit(0 if ok else 1)
PY
run_install --settings "$S" --uninstall --yes
[ "$RC" -eq 0 ] || ok=0
AP_FOREIGN="$FOREIGN29" python3 - "$S" <<'PY' || ok=0
import json, os, sys
data = json.load(open(sys.argv[1]))
ss = data["hooks"]["SessionStart"]
cmds = [h["command"] for g in ss for h in g.get("hooks", [])]
sys.exit(0 if cmds == [os.environ["AP_FOREIGN"]] else 1)
PY
if [ "$ok" -eq 1 ]; then
  pass "29 foreign command mentioning autopane path: untouched through install + uninstall"
else
  fail "29 foreign command mentioning autopane path" "see $OUT"
fi

# =============================================================== summary
echo ""
echo "$PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then exit 1; fi
exit 0
