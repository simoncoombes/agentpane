#!/bin/bash
# agentpane installer: wires "agentpane hook" into Claude Code's settings.json.
#
# Inspect first, print a plan plus warnings, confirm, back up, write atomically.
# Never touches any key other than "hooks", and inside "hooks" only appends its
# own {"matcher": "", "hooks": [...]} group per event (or removes exactly those
# entries with --uninstall). Existing hook groups always coexist and are left
# byte-for-byte alone.
#
# bash 3.2 compatible (macOS /bin/bash). All JSON work is done by embedded
# python3 -- jq is never assumed to exist.

set -euo pipefail

usage() {
  cat <<'EOF'
usage: ./install.sh [--dry-run] [--yes] [--uninstall] [--autopane] [--binary PATH] [--settings PATH] [--print-snippet]

Installs the agentpane hook entries (one appended group per event, 12 events)
into Claude Code's settings.json without touching anything else.

  --dry-run        print the plan and a unified diff, write nothing, exit 0
  --yes            skip the y/N confirmation (required when stdin is not a tty)
  --uninstall      remove agentpane entries only (hook + autopane), pruning
                   emptied groups and events but never the hooks key itself
  --autopane       also auto-open the TUI in an iTerm2 split when a session
                   starts: appends one extra SessionStart group (matcher
                   "startup|resume") running "agentpane autopane"; combined
                   with --uninstall it removes ONLY that entry
  --binary PATH    use this agentpane binary instead of auto-detection
  --settings PATH  target settings file (default: ~/.claude/settings.json)
  --print-snippet  print only the JSON snippet for manual pasting, then exit

exit codes: 0 success or nothing-to-do, 1 user abort/refusal, 2 environment error
EOF
}

# ---------------------------------------------------------------- flag parsing

DRY_RUN=0
ASSUME_YES=0
UNINSTALL=0
AUTOPANE=0
PRINT_SNIPPET=0
BINARY_FLAG=""
SETTINGS="$HOME/.claude/settings.json"

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)       DRY_RUN=1 ;;
    --yes)           ASSUME_YES=1 ;;
    --uninstall)     UNINSTALL=1 ;;
    --autopane)      AUTOPANE=1 ;;
    --print-snippet) PRINT_SNIPPET=1 ;;
    --binary)
      if [ $# -lt 2 ]; then echo "[error] --binary requires a path" >&2; exit 2; fi
      BINARY_FLAG="$2"; shift ;;
    --binary=*)      BINARY_FLAG="${1#*=}" ;;
    --settings)
      if [ $# -lt 2 ]; then echo "[error] --settings requires a path" >&2; exit 2; fi
      SETTINGS="$2"; shift ;;
    --settings=*)    SETTINGS="${1#*=}" ;;
    -h|--help)       usage; exit 0 ;;
    *)
      echo "[error] unknown argument: $1" >&2
      usage >&2
      exit 2 ;;
  esac
  shift
done

if ! command -v python3 >/dev/null 2>&1; then
  echo "[error] python3 is required (used for all JSON handling) and was not found" >&2
  exit 2
fi

abspath() {
  python3 -c 'import os, sys; print(os.path.abspath(os.path.expanduser(sys.argv[1])))' "$1"
}

SETTINGS="$(abspath "$SETTINGS")"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ------------------------------------------------------------- print-snippet

if [ "$PRINT_SNIPPET" -eq 1 ]; then
  # Resolve the binary quietly; fall back to the bare name if nothing is found.
  SNIP="$BINARY_FLAG"
  if [ -z "$SNIP" ]; then
    if command -v agentpane >/dev/null 2>&1; then
      SNIP="$(command -v agentpane)"
    elif [ -x "$HOME/go/bin/agentpane" ]; then
      SNIP="$HOME/go/bin/agentpane"
    else
      SNIP="agentpane"
    fi
  fi
  if [ "$SNIP" != "agentpane" ]; then
    SNIP="$(abspath "$SNIP")"
  fi
  AP_CMD="$SNIP" python3 - <<'PY'
import json, os
EVENTS = ["PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop",
          "SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
          "Notification", "PermissionRequest", "PreCompact", "PostCompact"]
cmd = os.environ["AP_CMD"] + " hook"
group = lambda: [{"matcher": "", "hooks": [{"type": "command", "command": cmd, "timeout": 5}]}]
print(json.dumps({"hooks": {e: group() for e in EVENTS}}, indent=2))
PY
  exit 0
fi

# ---------------------------------------------------------- binary resolution
# Not needed for --uninstall: removal matches any agentpane binary path.

BINARY=""
if [ "$UNINSTALL" -eq 0 ]; then
  if [ -n "$BINARY_FLAG" ]; then
    BINARY="$BINARY_FLAG"
  elif command -v agentpane >/dev/null 2>&1; then
    BINARY="$(command -v agentpane)"
  elif [ -x "$HOME/go/bin/agentpane" ]; then
    BINARY="$HOME/go/bin/agentpane"
  else
    GO_CMD=""
    if command -v go >/dev/null 2>&1; then
      GO_CMD="$(command -v go)"
    elif [ -x "$HOME/.local/share/go/bin/go" ]; then
      GO_CMD="$HOME/.local/share/go/bin/go"
    fi
    if [ -f "$SCRIPT_DIR/go.mod" ] && [ -d "$SCRIPT_DIR/cmd/agentpane" ] && [ -n "$GO_CMD" ]; then
      echo "[info] agentpane binary not found on PATH or at ~/go/bin/agentpane"
      DO_BUILD=0
      if [ "$ASSUME_YES" -eq 1 ]; then
        DO_BUILD=1
      elif [ -t 0 ]; then
        printf 'Run "%s install ./cmd/agentpane" from %s now? [y/N] ' "$GO_CMD" "$SCRIPT_DIR"
        reply=""
        read -r reply || reply=""
        case "$reply" in
          y|Y|yes|YES) DO_BUILD=1 ;;
          *) echo "[info] aborted; nothing installed"; exit 1 ;;
        esac
      else
        echo "[error] no agentpane binary found and stdin is not a tty; rerun with --yes to build, or pass --binary PATH" >&2
        exit 2
      fi
      if [ "$DO_BUILD" -eq 1 ]; then
        (cd "$SCRIPT_DIR" && "$GO_CMD" install ./cmd/agentpane)
        GOBIN="$("$GO_CMD" env GOBIN)"
        if [ -z "$GOBIN" ]; then
          GOBIN="$("$GO_CMD" env GOPATH)/bin"
        fi
        BINARY="$GOBIN/agentpane"
      fi
    else
      echo "[error] no agentpane binary found. Install one with:" >&2
      echo "[error]   go install github.com/simoncoombes/agentpane/cmd/agentpane@latest" >&2
      echo "[error] or point this script at one with --binary PATH" >&2
      exit 2
    fi
  fi

  BINARY="$(abspath "$BINARY")"
  if [ ! -x "$BINARY" ]; then
    echo "[error] $BINARY does not exist or is not executable" >&2
    exit 2
  fi
  # Idempotent re-runs and --uninstall recognize their own entries by the
  # ".../agentpane hook" command shape, so any other basename would duplicate
  # groups on every re-run and be orphaned by --uninstall.
  if [ "$(basename "$BINARY")" != "agentpane" ]; then
    echo "[error] $BINARY is not named \"agentpane\"; re-runs and --uninstall only recognize \".../agentpane hook\" commands" >&2
    echo "[error] rename or symlink it so the basename is exactly \"agentpane\", or omit --binary to auto-detect" >&2
    exit 2
  fi
  # Hooks run with an unpredictable PATH, so the absolute path above is what
  # gets written. Verify the binary actually executes before writing it in.
  if ! "$BINARY" version >/dev/null 2>&1; then
    echo "[error] \"$BINARY version\" failed to execute; refusing to install a broken hook command" >&2
    exit 2
  fi
  echo "[info] binary:   $BINARY"
fi

echo "[info] settings: $SETTINGS"

# ------------------------------------------------------------ inspect + plan

AP_WORK="$(mktemp -d "${TMPDIR:-/tmp}/agentpane-install.XXXXXX")"
trap 'rm -rf "$AP_WORK"' EXIT

export AP_SETTINGS="$SETTINGS"
export AP_BINARY="$BINARY"
export AP_AUTOPANE="$AUTOPANE"
export AP_WORK
if [ "$UNINSTALL" -eq 1 ]; then
  export AP_MODE="uninstall"
else
  export AP_MODE="install"
fi

# The plan phase never writes to the settings file. It prints the report
# ([warn]/[info]/[plan] lines) to stdout and leaves three files in AP_WORK:
#   status    one of: already | nothing | changes
#   proposed  the full proposed new settings content (when status=changes)
#   diff      a unified diff old -> proposed        (when status=changes)
# Malformed JSON aborts here with exit 2 before anything else can happen.
python3 - <<'PY'
import difflib, json, os, re, sys

settings = os.environ["AP_SETTINGS"]
binary   = os.environ.get("AP_BINARY", "")
mode     = os.environ["AP_MODE"]
work     = os.environ["AP_WORK"]
autopane = os.environ.get("AP_AUTOPANE", "0") == "1"

EVENTS = ["PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop",
          "SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
          "Notification", "PermissionRequest", "PreCompact", "PostCompact"]

# An agentpane entry is a command hook whose command STARTS WITH (after
# stripping) "agentpane hook"/"agentpane autopane" or "<any path>/agentpane
# hook"/".../agentpane autopane", optionally with trailing arguments.
# Anchored to the start so a foreign command that merely mentions an
# agentpane path (e.g. "notify.sh --after /x/agentpane hook") is never
# rewritten or removed. AGENTPANE_CMD (the combined form) is the plain
# --uninstall matcher; AGENTPANE_HOOK_CMD manages the 12 forwarder events;
# AGENTPANE_AUTOPANE_CMD manages the --autopane SessionStart entry (and is
# the --uninstall --autopane matcher).
#
# PARITY: the pattern strings below (AGENTPANE_CMD, AGENTPANE_HOOK_CMD,
# AGENTPANE_AUTOPANE_CMD, VISUALIZER, ALERTER, ALERTER_SAY) and the
# "^AGENT_PANE_" env prefix are mirrored as exported consts in
# cmd/agentpane/inspect.go, which gives `agentpane doctor` the same
# competing-settings diagnostics. TestInstallShParity
# (cmd/agentpane/inspect_test.go) asserts each string appears verbatim in
# this file; edit both files together.
AGENTPANE_CMD          = re.compile(r"^(?:\S*/)?agentpane +(?:hook|autopane)(?=\s|$)")
AGENTPANE_HOOK_CMD     = re.compile(r"^(?:\S*/)?agentpane +hook(?=\s|$)")
AGENTPANE_AUTOPANE_CMD = re.compile(r"^(?:\S*/)?agentpane +autopane(?=\s|$)")
VISUALIZER    = re.compile(r"pane|visual|dashboard|it2|iterm|tmux|wezterm|kitty", re.I)
ALERTER       = re.compile(r"osascript|notifier|bell|afplay", re.I)
ALERTER_SAY   = re.compile(r"(^|[^A-Za-z])say([^A-Za-z]|$)", re.I)

def out(prefix, msg):
    print("[%s] %s" % (prefix, msg))

def put(name, text):
    with open(os.path.join(work, name), "w") as f:
        f.write(text)

def is_agentpane(cmd):
    return bool(AGENTPANE_CMD.match(cmd.strip()))

def is_hook(cmd):
    return bool(AGENTPANE_HOOK_CMD.match(cmd.strip()))

def is_autopane(cmd):
    return bool(AGENTPANE_AUTOPANE_CMD.match(cmd.strip()))

# ---- load ----
exists = os.path.lexists(settings)
if os.path.islink(settings) and not os.path.exists(settings):
    sys.stderr.write("[error] %s is a symlink to a missing target (%s); fix the link first\n"
                     % (settings, os.path.realpath(settings)))
    sys.exit(2)

old_text = ""
if exists:
    with open(settings) as f:
        old_text = f.read()
    def no_dup_keys(pairs):
        # json.loads silently keeps only the last value for a duplicated key,
        # so a rewrite would drop data the file visibly contains. Refuse.
        d = {}
        for k, v in pairs:
            if k in d:
                raise ValueError("duplicate key %s" % json.dumps(k))
            d[k] = v
        return d
    try:
        data = json.loads(old_text, object_pairs_hook=no_dup_keys)
    except ValueError as e:
        sys.stderr.write("[error] %s is not valid JSON (%s)\n" % (settings, e))
        sys.stderr.write("[error] refusing to touch it; fix the file by hand and rerun\n")
        sys.exit(2)
    if not isinstance(data, dict):
        sys.stderr.write("[error] %s does not contain a JSON object at the top level; refusing to touch it\n" % settings)
        sys.exit(2)
    if "hooks" in data and not isinstance(data["hooks"], dict):
        sys.stderr.write("[error] the \"hooks\" key in %s is not an object; refusing to touch it\n" % settings)
        sys.exit(2)
    if isinstance(data.get("hooks"), dict):
        for ev in EVENTS:
            if ev in data["hooks"] and not isinstance(data["hooks"][ev], list):
                sys.stderr.write("[error] hooks.%s in %s is not a list; refusing to touch it\n" % (ev, settings))
                sys.exit(2)
else:
    data = {}

# ---- warning: settings managed by another system (symlink) ----
if os.path.islink(settings):
    target = os.path.realpath(settings)
    out("warn", "%s is a symlink -> %s" % (settings, target))
    out("warn", "these settings look managed by another system (a dotfiles repo or fleet-style installer)")
    out("warn", "this edit will land in %s; that system's installer may overwrite it," % target)
    out("warn", "or the change may show up as an uncommitted diff in its repo -- consider committing it there too")

hooks = data.get("hooks") if isinstance(data.get("hooks"), dict) else {}

def entries(event):
    groups = hooks.get(event)
    if not isinstance(groups, list):
        return
    for gi, g in enumerate(groups):
        if not isinstance(g, dict):
            continue
        hl = g.get("hooks")
        if not isinstance(hl, list):
            continue
        for hi, h in enumerate(hl):
            if isinstance(h, dict) and h.get("type") == "command":
                yield gi, hi, str(h.get("command") or "")

if mode == "install":
    # ---- warning: competing subagent visualizers ----
    env_block = data.get("env") if isinstance(data.get("env"), dict) else {}
    ap_env = sorted(k for k in env_block if re.match(r"^AGENT_PANE_", str(k)))
    for ev in ("SubagentStart", "SubagentStop", "PreToolUse"):
        for gi, hi, cmd in entries(ev):
            if is_agentpane(cmd):
                continue
            if VISUALIZER.search(cmd):
                out("warn", "competing subagent visualizer on %s: %s" % (ev, cmd))
                out("warn", "running two subagent UIs doubles panes and notifications; consider disabling one")
                if ap_env:
                    out("warn", "env vars %s in this settings file suggest that tool has its own on/off switch there"
                        % ", ".join(ap_env))

    # ---- warning: duplicate alerting ----
    for ev in ("Notification", "Stop"):
        for gi, hi, cmd in entries(ev):
            if is_agentpane(cmd):
                continue
            if ALERTER.search(cmd) or ALERTER_SAY.search(cmd):
                out("warn", "existing alerting hook on %s: %s" % (ev, cmd))
                out("warn", "agentpane also rings/badges on permission prompts, so expect double alerts;")
                out("warn", "see agentpane --no-bell / --no-badge / --no-notify or the bell/badge/notify config keys")

    # ---- info: status line ----
    if "statusLine" in data:
        out("info", "a statusLine is configured; agentpane --oneline can replace or double a status line")

    # ---- info: coexisting hooks on the 12 events ----
    coexist = []
    for ev in EVENTS:
        for gi, hi, cmd in entries(ev):
            if not is_agentpane(cmd):
                coexist.append((ev, cmd))
    if coexist:
        out("info", "coexisting hooks left untouched (hook groups coexist by design; agentpane is observe-only and appended last):")
        for ev, cmd in coexist:
            out("info", "  %s: %s" % (ev, cmd))

    # ---- plan ----
    expected      = binary + " hook"
    expected_auto = binary + " autopane"

    def rebind(cmd, pattern, target):
        # Rewrite only the binary path before the verb; any user-added
        # trailing arguments are preserved verbatim.
        s = cmd.strip()
        return target + s[pattern.match(s).end():]

    to_add = []
    to_update = []  # (event, gi, hi, old command, new command)
    to_remove = []  # (event, gi, hi, old command) -- exact duplicates
    for ev in EVENTS:
        seen = []
        covered = False
        for gi, hi, cmd in entries(ev):
            if not is_hook(cmd):
                continue
            covered = True
            new = rebind(cmd, AGENTPANE_HOOK_CMD, expected)
            if new in seen:
                # A second entry that rebinds to the same command (e.g. a
                # stale old-path group next to a current one) would become an
                # exact duplicate; remove it instead of leaving it stale.
                to_remove.append((ev, gi, hi, cmd))
                continue
            seen.append(new)
            if new != cmd:
                to_update.append((ev, gi, hi, cmd, new))
        if not covered:
            to_add.append(ev)

    # Autopane entry (SessionStart only). Existing entries get the same
    # path-rebind + exact-duplicate treatment on EVERY install run, so a
    # rebuilt binary never leaves a stale auto-open behind; --autopane only
    # controls whether a missing entry is ADDED.
    auto_add = False
    auto_covered = False
    auto_seen = []
    for gi, hi, cmd in entries("SessionStart"):
        if not is_autopane(cmd):
            continue
        auto_covered = True
        new = rebind(cmd, AGENTPANE_AUTOPANE_CMD, expected_auto)
        if new in auto_seen:
            to_remove.append(("SessionStart", gi, hi, cmd))
            continue
        auto_seen.append(new)
        if new != cmd:
            to_update.append(("SessionStart", gi, hi, cmd, new))
    if autopane and not auto_covered:
        auto_add = True

    if not to_add and not to_update and not to_remove and not auto_add:
        if autopane:
            out("info", "agentpane hooks (all 12 events) and the autopane entry already installed with this binary; nothing to do")
        else:
            out("info", "agentpane hooks already installed for all 12 events with this binary; nothing to do")
        put("status", "already")
        sys.exit(0)

    if not exists:
        out("plan", "create %s containing only the hooks block" % settings)

    if "hooks" not in data:
        data["hooks"] = {}
    dh = data["hooks"]

    for ev, gi, hi, oldcmd, newcmd in to_update:
        dh[ev][gi]["hooks"][hi]["command"] = newcmd
        out("plan", "update agentpane binary path on %s: \"%s\" -> \"%s\"" % (ev, oldcmd, newcmd))

    # Removals go after in-place updates (updates rely on original indices)
    # and in descending (gi, hi) order per event so earlier indices stay
    # valid while deleting.
    for ev, gi, hi, oldcmd in sorted(to_remove, key=lambda x: (x[0], x[1], x[2]), reverse=True):
        del dh[ev][gi]["hooks"][hi]
        out("plan", "remove duplicate agentpane hook from %s (command: %s)" % (ev, oldcmd))
    for ev in sorted(set(e for e, _, _, _ in to_remove)):
        dh[ev] = [g for g in dh[ev]
                  if not (isinstance(g, dict) and g.get("hooks") == []
                          and set(g.keys()) <= {"matcher", "hooks"})]

    for ev in to_add:
        arr = dh.get(ev)
        if not isinstance(arr, list):
            arr = []
            dh[ev] = arr
        arr.append({"matcher": "", "hooks": [{"type": "command", "command": expected, "timeout": 5}]})
        out("plan", 'append to %s: {"matcher": "", "hooks": [{"type": "command", "command": "%s", "timeout": 5}]}'
            % (ev, expected))

    if auto_add:
        arr = dh.get("SessionStart")
        if not isinstance(arr, list):
            arr = []
            dh["SessionStart"] = arr
        arr.append({"matcher": "startup|resume", "hooks": [{"type": "command", "command": expected_auto, "timeout": 5}]})
        out("plan", 'append to SessionStart: {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "%s", "timeout": 5}]}'
            % expected_auto)

else:
    # ---- uninstall ----
    if not exists:
        out("info", "%s does not exist; nothing to uninstall" % settings)
        put("status", "nothing")
        sys.exit(0)

    # Plain --uninstall removes every agentpane entry (hook AND autopane);
    # --uninstall --autopane removes only the autopane entry.
    matches = is_autopane if autopane else is_agentpane

    removed = 0
    dh = data.get("hooks")
    if isinstance(dh, dict):
        for ev in list(dh.keys()):
            groups = dh[ev]
            if not isinstance(groups, list):
                continue
            new_groups = []
            ev_changed = False
            for g in groups:
                if isinstance(g, dict) and isinstance(g.get("hooks"), list):
                    kept = []
                    for h in g["hooks"]:
                        if (isinstance(h, dict) and h.get("type") == "command"
                                and matches(str(h.get("command") or ""))):
                            removed += 1
                            ev_changed = True
                            out("plan", "remove agentpane hook from %s (command: %s)" % (ev, h.get("command")))
                        else:
                            kept.append(h)
                    if len(kept) != len(g["hooks"]):
                        if not kept and set(g.keys()) <= {"matcher", "hooks"}:
                            continue  # drop the emptied group entirely
                        g["hooks"] = kept
                new_groups.append(g)
            if ev_changed:
                if new_groups:
                    dh[ev] = new_groups
                else:
                    del dh[ev]
                    out("plan", "remove emptied event %s (the hooks key itself is kept)" % ev)

    if removed == 0:
        out("info", "no agentpane hook entries found in %s; nothing to do" % settings)
        put("status", "nothing")
        sys.exit(0)

# ---- serialize the proposal: key order preserved; reuse the file's own
# indent unit (first indented line after the opening brace) so a 4-space or
# tab-indented file does not get a whole-file reformat diff ----
indent = 2
m = re.match(r"\s*[\{\[][ \t]*\r?\n([ \t]+)", old_text)
if m:
    indent = m.group(1)
new_text = json.dumps(data, indent=indent, ensure_ascii=False) + "\n"
put("proposed", new_text)
put("diff", "".join(difflib.unified_diff(
    old_text.splitlines(True), new_text.splitlines(True),
    fromfile=settings, tofile=settings + " (proposed)")))
put("status", "changes")
PY

STATUS="$(cat "$AP_WORK/status")"

if [ "$STATUS" = "already" ] || [ "$STATUS" = "nothing" ]; then
  exit 0
fi

# ---------------------------------------------------------------- dry run

if [ "$DRY_RUN" -eq 1 ]; then
  echo "[plan] proposed diff:"
  cat "$AP_WORK/diff"
  echo "[info] dry run: nothing was written"
  exit 0
fi

# ---------------------------------------------------------------- confirm

if [ "$ASSUME_YES" -ne 1 ]; then
  if [ ! -t 0 ]; then
    echo "[error] stdin is not a tty; refusing to write without --yes (use --dry-run to preview)" >&2
    exit 1
  fi
  printf 'Apply these changes to %s? [y/N] ' "$SETTINGS"
  reply=""
  read -r reply || reply=""
  case "$reply" in
    y|Y|yes|YES) ;;
    *) echo "[info] aborted; nothing written"; exit 1 ;;
  esac
fi

# ---------------------------------------------------------------- write
# Backup lands next to the RESOLVED file (through any symlink chain), and the
# write is tmp + os.replace in that same directory, so the symlink itself is
# never replaced and the write is atomic.

python3 - <<'PY'
import os, shutil, sys, time

settings = os.environ["AP_SETTINGS"]
work     = os.environ["AP_WORK"]

try:
    resolved = os.path.realpath(settings)
    d = os.path.dirname(resolved) or "."
    if not os.path.isdir(d):
        os.makedirs(d)

    if os.path.exists(resolved):
        backup = os.path.join(d, os.path.basename(resolved)
                              + ".agentpane-backup-" + time.strftime("%Y%m%d-%H%M%S"))
        base = backup
        n = 0
        while os.path.exists(backup):
            n += 1
            backup = "%s.%d" % (base, n)
        shutil.copy2(resolved, backup)
        print("[info] backup written: %s" % backup)

    with open(os.path.join(work, "proposed")) as f:
        content = f.read()

    tmp = os.path.join(d, ".%s.agentpane-tmp-%d" % (os.path.basename(resolved), os.getpid()))
    try:
        with open(tmp, "w") as f:
            f.write(content)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, resolved)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise
    print("[info] wrote %s" % resolved)
except OSError as e:
    sys.stderr.write("[error] failed to write %s: %s\n" % (settings, e))
    sys.exit(2)
PY

if [ "$UNINSTALL" -eq 1 ]; then
  if [ "$AUTOPANE" -eq 1 ]; then
    echo "[info] agentpane autopane entry removed; hook entries and everything else were left untouched"
  else
    echo "[info] agentpane hook entries removed; everything else was left untouched"
  fi
else
  echo "[info] hooks are re-read per invocation: running Claude Code sessions pick this up on their next tool call, no restart needed"
  echo "[info] run \"agentpane doctor\" to verify the installation"
fi
exit 0
