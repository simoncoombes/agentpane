# agentpane

A read-only terminal monitor for Claude Code subagents. It runs in a second
pane next to your Claude Code session and renders the agent tree live, so
three questions are answered without reading prose:

1. Where is every agent in its work?
2. Is anything stuck?
3. What is it doing right now?

It is not a chat client. It never sends prompts to Claude, never writes to
your repo or session, and cannot terminate anything. It observes and it
points. The loudest thing it does is tell you an agent is blocked waiting for
your approval, which is the expensive failure mode when Claude fans out and
you are looking elsewhere.

Single static Go binary, ANSI 16-color only (it inherits your terminal
scheme), no Nerd Font required.

## Install

```sh
go install github.com/simoncoombes/agentpane/cmd/agentpane@latest
```

Or from source:

```sh
git clone https://github.com/simoncoombes/agentpane
cd agentpane
go build -o agentpane ./cmd/agentpane
```

Make sure the binary lands on your `PATH` (the hook invokes it by name).

## Install script

`./install.sh` wires the hooks into `~/.claude/settings.json` for you. It
finds the binary, inspects your existing settings first (symlinked dotfiles
setups, competing subagent pane visualizers, notification hooks that would
double up alerts, a configured status line), prints a plan plus warnings, and
only after a y/N confirm appends one `agentpane hook` group per event, using
the absolute binary path. It backs the file up first, writes atomically, and
never touches any key except `hooks`; existing hook groups coexist untouched,
with agentpane's entry appended last. Re-runs are idempotent: an existing
`agentpane hook` entry gets its binary path updated in place (any extra
arguments you added to the command are kept), exact duplicates are pruned,
and the file keeps its own indentation. The binary must be named `agentpane`;
that is how re-runs and `--uninstall` recognize the installer's own entries.

```sh
./install.sh                 # inspect, warn, confirm, write
./install.sh --autopane      # same, plus auto-open the TUI on session start
./install.sh --dry-run       # plan + unified diff, writes nothing
./install.sh --uninstall     # remove only agentpane's entries (hook + autopane)
./install.sh --print-snippet # just the JSON, for manual pasting
```

Other flags: `--yes` (skip the prompt; required when stdin is not a tty),
`--binary PATH`, `--settings PATH`.

The same inspection is available permanently, read-only, as the "competing
settings" section of `agentpane doctor`: symlinked/managed settings (including
a symlink whose target is gone), competing subagent visualizers, pane-tool env
switches, alerting hooks that would double up, statusLine overlap, malformed
or duplicate-keyed JSON, plus richer checks on agentpane's own entries (event
coverage, stale binary paths, duplicates, ordering). The detection patterns
are shared with `install.sh` and a parity test (`TestInstallShParity`) keeps
the two from drifting.

Prefer to do it by hand? The exact snippet is in
[The hook snippet](#the-hook-snippet) below, and `agentpane doctor` prints it
too when the hooks are absent.

## Auto-open (iTerm2)

`./install.sh --autopane` adds one extra `SessionStart` hook group (matcher
`startup|resume`) running `agentpane autopane`. From then on, starting an
interactive Claude Code session in an iTerm2 pane automatically splits that
pane vertically and runs the agentpane TUI in the new pane — no arrangement,
no manual split. This is the recommended iTerm2 setup; see
[docs/ITERM2.md](docs/ITERM2.md) for the full walkthrough.

`agentpane autopane` is silent and exits 0 on every path. It opens a pane
only when ALL of these hold, and skips silently otherwise:

- the payload is a real `SessionStart` with source `startup` or `resume`
  (`clear`/`compact`/`fork` re-fire mid-session and never open panes);
- `AGENTPANE_AUTOPANE` is not `"0"` — the kill switch: `export
  AGENTPANE_AUTOPANE=0` disables auto-open without uninstalling;
- the session runs in an iTerm2 pane (`TERM_PROGRAM` is `iTerm.app` and
  `ITERM_SESSION_ID` is set — that id is how the split targets the exact
  pane the session lives in, even if you have focused another window);
- the session is not nested inside another Claude session
  (`CLAUDE_CODE_CHILD_SESSION` unset);
- the `claude` process has a controlling terminal (a fully headless run —
  cron, launchd, CI, a detached pipeline — gets no pane; note that
  `claude -p` typed into a terminal still has one, so use the kill switch
  around scripted `-p` loops);
- no agentpane TUI is already attached to the session (its socket exists);
- a per-session lockfile wasn't just taken by a duplicate event (stale locks
  older than 1h are reclaimed).

The new pane uses the iTerm2 profile named by `AGENTPANE_PROFILE` (default
`AgentPane`, the one docs/ITERM2.md ships via Dynamic Profiles) and falls
back to the current session's own profile when it does not exist.

**Autopane panes are pinned.** The pane runs `agentpane --session <id>` by
absolute binary path, with the session id from the SessionStart payload, so
for its whole life it watches the session in the tab it was split from and
no other. It never re-attaches, its idle screen has no session picker, and
`1`-`9` do nothing there. If the pane opens before that session's registry
row lands (it is split *during* SessionStart, so this is common), it waits
and says `waiting for session <id>` rather than attaching to a neighbour.

This is a deliberate deviation from SPEC §3.8a, which attaches by "newest
live session whose cwd matches" and always lists the alternatives. With
several Claude sessions running in one directory that rule is a coin flip,
and a monitor watching the wrong session looks completely plausible while
being wrong. So the picker survives only where it is a recovery path rather
than a trap: an unpinned `agentpane` with more than one live session to
choose between.

Remove just the auto-open entry with `./install.sh --uninstall --autopane`;
plain `./install.sh --uninstall` removes it along with the hook entries.
`agentpane doctor` reports whether autopane is installed.

## Quickstart

First, see the UI with no Claude Code session at all:

```sh
agentpane --demo
```

This replays a canned scenario (SPEC §2.10): a blocked permission ask, a
stalled agent, a long open Bash call, a decayed
finished agent, a queued agent, and a low-telemetry teammate. `p` pauses the
replay, `.` steps one event, `--demo-speed 2.0` and `--demo-seek 90` jump
around. Everything the live view can show is reachable here.

To attach to a real session:

1. Run `agentpane doctor`. It checks your environment and, if the hooks are
   not installed, prints the exact `settings.json` snippet to paste (also
   reproduced below). It also scans your settings read-only for competing
   subagent visualizers, duplicate alerting hooks, and symlinked/managed
   settings — the same warnings `./install.sh` prints before writing.
2. Add the hook snippet to `~/.claude/settings.json`. Hooks are re-read per
   invocation, so already-running sessions pick it up without a restart.
3. Open a second terminal pane in the same directory as your Claude Code
   session and run `agentpane`. It attaches to the newest live session whose
   cwd matches. When several sessions exist, the idle screen lists them and
   `1`-`9` switches. To skip the guess entirely, name the session:
   `agentpane --session <id>` attaches to exactly that one, waits for it if
   it has not started yet, and never moves. That is what the auto-open pane
   does for you.

For the iTerm2 profile and split-pane setup, see
[docs/ITERM2.md](docs/ITERM2.md).

## The hook snippet

`agentpane hook` reads one hook JSON document on stdin, forwards it over a
per-session unix socket to the running TUI, and exits 0 always. If no TUI is
running it exits 0 silently, so it can never block or fail a tool call.

Paste this into the `"hooks"` section of `~/.claude/settings.json` (this is
exactly what `agentpane doctor` prints when the hooks are absent). If you
already have hooks configured, append the `agentpane hook` entry to each
event's existing array rather than replacing it:

```json
{
  "hooks": {
    "PreToolUse": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "PostToolUse": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "SubagentStart": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "SubagentStop": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "SessionEnd": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "Notification": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "PermissionRequest": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "PreCompact": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ],
    "PostCompact": [
      { "hooks": [ { "type": "command", "command": "agentpane hook", "timeout": 5 } ] }
    ]
  }
}
```

## The main row

The root of the tree is one line:

```
◍ main  Build agentpane — live TUI monitor for Claude Code… 112k
│ write the migration                                         5s
```

The text next to `main` is a short **workstream label**, not your prompt. It
is taken, in order, from:

1. Claude Code's own session title (the one it shows in its tab), read from
   the transcript;
2. the current run's prompt, if no title has been written yet;
3. main's most recent observed activity;
4. nothing, if none of the above has been seen. agentpane never writes a
   summary of its own.

The label is then fitted to whatever space is left after `◍ main` and the
token count, cut on a word boundary with `…`. It is the first thing to give
way as the pane narrows - the token column never moves and the row never
wraps. The full untruncated text is always one keystroke away: select `main`
and press `⏎` to see it in the inspector, or `y` to copy it.

The second line is main's current todo item and how long since it last did
anything.

## How it gets its data

Two sources, behind one interface:

- **Hooks (primary).** Claude Code hook events carry per-tool activity with
  subagent attribution (`agent_id` on subagent events) in real time, and the
  `PermissionRequest` hook fires within ~64 ms of a blocked call with the
  exact tool and command. `agentpane hook` forwards each event to the TUI's
  socket at `$TMPDIR/agentpane-<session>.sock`.
- **Transcript tailing (fallback).** The JSONL transcripts under
  `~/.claude/projects/` are the only place tool outputs, token counts, and
  permission-denial records live. Each subagent writes its own file, which the
  TUI tails. This works with zero installation but is slower (seconds, not
  milliseconds) and cannot see a pending permission prompt. It is also the
  only channel carrying the session title on main's row - no hook reports it,
  so without transcript access the row falls back to the run prompt.

The full machine-verified survey of every channel, payload shape, and latency
is in [docs/DATA-SOURCES.md](docs/DATA-SOURCES.md). If the source dies,
agentpane shows a disconnected state and reconnects with backoff; it never
crashes and never invents data it did not observe.

## CLI

| Invocation | Does |
|---|---|
| `agentpane` | attach to the current directory's newest live session, render live |
| `agentpane --session <id>` | attach to exactly this session id and never guess: no cwd rule, no session picker, waits if the id has not reached the registry yet (auto-open panes are launched this way). An empty value is refused - an unset variable must not silently become a guess |
| `agentpane --demo` | replay the §2.10 scenario, no Claude needed |
| `agentpane --demo-speed 2.0` | demo playback speed multiplier |
| `agentpane --demo-seek 90` | jump the demo to t=90s |
| `agentpane hook` | read one hook JSON on stdin, forward, exit 0 always |
| `agentpane autopane` | SessionStart hook: auto-open the TUI in an iTerm2 split (see [Auto-open](#auto-open-iterm2)), exit 0 always |
| `agentpane doctor` | check hooks, competing settings (visualizers, alerting, symlinks), socket, terminal escapes, glyphs; print the hook snippet if missing |
| `agentpane doctor --session <id>` | the same checks aimed at one pinned pane: that session's attach target and socket, no cwd guess |
| `agentpane runs` | list persisted runs from every session |
| `agentpane runs --session <id>` | list only that session's runs (what its pane shows) |
| `agentpane runs show <id>` | print one run summary |
| `agentpane --oneline` | single-row status output for a tmux/iTerm2 status bar, then exit |
| `agentpane --oneline --session <id>` | the same row for exactly one session's pane |
| `agentpane --width narrow` | start in the 44-column layout (`wide` is the 64-column default) |
| `agentpane --accent auto\|2\|3\|4\|5\|6` | force the live-accent ANSI index instead of the auto-pick |
| `agentpane --no-color` | disable all color (the `NO_COLOR` env var does the same) |
| `agentpane --no-bell --no-badge --no-notify --no-links` | switch off individual side effects |
| `agentpane --log <path>` | debug log |
| `agentpane --source hook\|transcript\|fake` | force one source |
| `agentpane --render-once --cols 64 --rows 40` | print one frame of the built-in demo scenario to stdout and exit (CI, screenshots); rejected with `--session`, which it cannot honor |
| `agentpane version` | print the version |

## Config

`~/.config/agentpane/config.toml` (honors `$XDG_CONFIG_HOME`). Every key is
optional; these are the defaults. Invalid values warn once in the footer and
fall back to the default, never exit.

```toml
width = "wide"              # wide | narrow
stuck_after = "45s"
long_running_after = "3m"
long_running_allow = ["test", "build", "install", "compile", "bundle", "migrate"]
watch_never_exempt = true   # a quiet --watch is always stuck
decay_after = "30s"
bell = true
badge = true
title = true
time_mode = "since"         # since | absolute
editor = ""                 # falls back to $EDITOR
spark = "tokens"          # tokens | calls - what the sparkline plots
max_calls_shown = 4        # window into the call ring buffer; overflow shows "+n more"
history_runs = 20         # runs kept per session under runs/s-<session-id>/
notify = true             # OSC 9 system notification for an unanswered ask
notify_after = "60s"
attention = true          # dock bounce on a new ask
links = true              # OSC 8 hyperlinks on paths
focus_digest = true       # away-digest via focus reporting
digest_cap = 3            # changes listed before "+n more"
slug_max = 18             # name slug budget
accent = "auto"           # auto | 2 | 3 | 4 | 5 | 6  (ANSI index)
                          # needs-you has NO colour setting - it is bright + reverse + ⚑
color = true              # false == NO_COLOR
```

## Keys

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | select next / previous (no wrap; cancels follow) |
| `g` / `G` | first / last row |
| `space` | pin/unpin the selected agent's tool calls (no-op on decayed/queued) |
| `⏎` | open the inspector; on the condensed screen's alert row, focus the left pane instead |
| `esc` / `q` | close the inspector - never kills anything; `q` never quits the pane |
| `f` | follow mode: inspector tracks the most recently active agent |
| `o` | open the selected agent's current file in `$EDITOR` (new iTerm2 tab) |
| `y` | yank the agent's error, else its failing command, else its activity line; on `main`, the full workstream label |
| `Y` | yank the whole inspector log; on `main` with an empty log, the full workstream label |
| `w` | toggle 64/44 layout (persisted) |
| `[` / `]` | previous / next run in idle or history view |
| `1`-`9` | switch attached session (idle screen, only when unpinned and more than one session is live; no-op otherwise) |
| `t` | cycle the right column: since-last-event, token burn rate (persisted) |
| `⇞` / `⇟` | page the inspector when it is open |
| `esc` (on the digest) | dismiss the away-digest change marks |
| `?` | key help overlay, dropped-event count, source name |
| `ctrl-c` | quit agentpane; agents keep running (asks to confirm when agents are live) |
| `p` / `.` | demo only: pause/resume, step one event |

There is no kill key. Pressing `x` explains why:
`read-only — subagents cannot be killed`.

## Below the tree: nothing

The tree owns every row between the header and the inspector. There is no
narration, no alert band and no contention block under it - those regions were
removed, and what was a negotiation over the leftover rows is now a subtraction.
A pane that used to spend ten or more rows on prose spends them on agents.

The alerts themselves are not gone, only their region. Three surfaces still
carry them:

- the header's `⚑ n NEEDS YOU` count, which is the first thing read;
- the `⚑` glyph and `blocked · <what it asked for>` line on the agent's own
  tree row;
- the inspector, which states the exact command the agent is waiting on.

Under 12 rows the condensed screen still draws a single one-line alert directly
under the header (`⚑ <slug> permission ×3`), because at that height the tree
cannot say it. `⏎` there jumps focus to the left pane.

## Status bar (`--oneline`)

Each running TUI writes a small state file to
`~/.local/state/agentpane/current-<session-id>.json` on every change
(debounced to 1/s). The file is per session because panes are pinned to one:
several panes are several writers, and a single shared file would make the
status bar flip between unrelated sessions second to second.
`agentpane --oneline` reads one, prints one row, and exits:

```
⚑1 ●4 ◐1 ✔2 4m02s fix-ts2345
```

asks, live agents, stalls (`○n`, only when present), open calls, settled, and
the oldest ask's age and slug. If nothing is fresh (no file, or all of them
older than 15s) it prints `no agentpane running` and still exits 0, so a
status bar never shows an error.

With no `--session`, `--oneline` reports the most recently updated pane and
prefixes the row with that session's short id whenever more than one pane is
live, so the row is never anonymous:

```
2a71 ⚑1 ●4 ✔2 4m02s fix-ts2345
```

`agentpane --oneline --session <id>` reports exactly that pane instead, with
no fallback: if that session is not running, the row is `no agentpane
running`, even while other panes are.

tmux (whole machine, newest pane):

```
set -g status-interval 5
set -g status-right '#(agentpane --oneline)'
```

tmux (one pinned session, e.g. per window):

```
set -g status-right '#(agentpane --oneline --session "$CLAUDE_CODE_SESSION_ID")'
```

`CLAUDE_CODE_SESSION_ID` is the variable Claude Code exports into its own
session's shell; it holds the same id the registry (and `agentpane autopane`)
uses. Quote it: an unquoted expansion of an unset variable feeds `--session`
the next argument, or nothing at all. Either way `--oneline` answers with one
short line and exit 0 — it never prints a usage block into a status bar — but
the row is then a parse error rather than your session's status, so if that is
what you see, check the variable is set in the shell tmux runs the command in.
An empty `--session` is refused rather than quietly falling back to guessing
this directory's newest session.

iTerm2: Settings > Profiles > Session > enable "Status bar enabled" >
Configure Status Bar, drag in the **Command** component, set the command to
the absolute binary path (for example `/Users/you/go/bin/agentpane --oneline`;
the status bar does not use your login `PATH`) and an update cadence of a few
seconds.

## Honest limitations

agentpane never invents data. Where the platform emits nothing, it renders
the absence, which means:

- **No kill.** Subagents run in-process inside `claude`; there is no per-agent
  process and no termination mechanism. agentpane is strictly read-only.
- **No failure verdicts.** The subagent-finished event carries no
  status/error/exit code, so there is no `failed` state. A returned agent is
  `done`, and its last message is shown (in the error color when it looks like
  an error), with no verdict added.
- **Teammates have almost no telemetry.** Named agents emit no lifecycle or
  per-tool events. They render as a distinct `◇` row with `limited telemetry`,
  no pips, no sparkline, no liveness clock, and they never trigger alerts.
- **Denials clear slowly.** Granting a permission produces an event within
  ~1s; denying with Esc produces nothing. After you answer, the row shows
  `resolving…` and clears on the agent's next event or a short grace period.
  It never claims an outcome it did not observe.
- **Some markers are inferred, and look it.** The `↺n` retry counter and the
  `✓ verified` marker are heuristics over Bash commands; they render dim and
  are labeled `(inferred)` in the inspector. Stall detection is a real timer,
  not an inference.
- **The token sparkline is coarse.** Token usage arrives once per assistant
  message, not incrementally, so each message lands in a single bucket. Agents
  with no token stream fall back to plotting call cadence.
- **Escalation fires only for permission asks.** A stall or a completion never
  produces a notification or dock bounce, so the notification keeps meaning
  something.

Details and evidence for all of the above:
[docs/DATA-SOURCES.md](docs/DATA-SOURCES.md).
