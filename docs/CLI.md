# CLI, config and keys

The reference for every flag, every key and every setting. The README has the
handful you need on day one.

## CLI

| Invocation | Does |
|---|---|
| `agentpane` | attach to the current directory's newest live session, render live |
| `agentpane --session <id>` | attach to exactly this session id and never guess: no cwd rule, no session picker, waits if the id has not reached the registry yet (auto-open panes are launched this way). An empty value is refused - an unset variable must not silently become a guess |
| `agentpane --demo` | replay the §2.10 scenario, no Claude needed |
| `agentpane --demo-speed 2.0` | demo playback speed multiplier |
| `agentpane --demo-seek 90` | jump the demo to t=90s |
| `agentpane hook` | read one hook JSON on stdin, forward, exit 0 always |
| `agentpane autopane` | SessionStart hook: auto-open the TUI in a terminal split (see [INSTALL.md](INSTALL.md)), exit 0 always |
| `agentpane autopane --explain` | run the same guard chain against the current environment and print each verdict, without opening anything |
| `agentpane doctor` | check hooks, competing settings (visualizers, alerting, symlinks), socket, terminal escapes, glyphs; print the hook snippet if missing |
| `agentpane doctor --session <id>` | the same checks aimed at one pinned pane: that session's attach target and socket, no cwd guess |
| `agentpane runs` | list persisted runs from every session |
| `agentpane runs --session <id>` | list only that session's runs |
| `agentpane runs show <id>` | print one run summary |
| `agentpane --oneline` | single-row status output for a tmux/iTerm2 status bar, then exit |
| `agentpane --oneline --session <id>` | the same row for exactly one session's pane |
| `agentpane --width wide\|narrow` | fix the layout at 64 or 44 columns instead of the default, which is the pane's own width up to `max_width` |
| `agentpane --accent auto\|2\|3\|4\|5\|6` | force the live-accent ANSI index instead of the auto-pick |
| `agentpane --no-color` | disable all color (the `NO_COLOR` env var does the same) |
| `agentpane --no-bell --no-badge --no-notify --no-links` | switch off individual side effects |
| `agentpane --log <path>` | debug log - including one line per frame for any transition in flight (`anim enter id=… d=…ms`), which is how to tell whether the pane thinks it is animating |
| `agentpane --source hook\|transcript\|fake\|codex` | force one source; `codex` watches an OpenAI Codex run |
| `agentpane --render-once --cols 64 --rows 40` | print one frame of the built-in demo scenario to stdout and exit (CI, screenshots); rejected with `--session`, which it cannot honor |
| `agentpane version` | print the version |

## Config

`~/.config/agentpane/config.toml` (honors `$XDG_CONFIG_HOME`). Every key is
optional; these are the defaults. Invalid values warn once in the footer and
fall back to the default, never exit.

```toml
width = "fill"              # fill (the pane) | wide (64) | narrow (44)
max_width = 100             # where fill stops; at least 44
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
max_calls_shown = 4        # history kept on a row PINNED with `space`;
                           # every other row shows its activity line alone
history_runs = 20         # runs kept per session under runs/s-<session-id>/
                          # (written for `agentpane runs`; the pane itself
                          #  only ever shows the run it just watched end)
notify = true             # OSC 9 system notification for an unanswered ask
notify_after = "60s"
attention = true          # dock bounce on a new ask
links = true              # OSC 8 hyperlinks on paths
focus_digest = true       # away-digest via focus reporting
digest_cap = 3            # changes listed before "+n more"
slug_max = 18             # name slug budget
accent = "auto"           # auto | 2 | 3 | 4 | 5 | 6  (ANSI index)
                          # needs-you has NO colour setting - it is bright + reverse + ⚑
                          # model/effort use ANSI 12 (bright blue), or 14 if the
                          # accent is blue - not configurable
color = true              # false == NO_COLOR
```

## Keys

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | select next / previous (no wrap; cancels follow) |
| `g` / `G` | first / last row |
| `space` | pin/unpin this agent's history onto the tree - `⏎` inspects instead (no-op on decayed/queued) |
| `⏎` | open the inspector; on the `RETURNED` header, toggle those rows; on the condensed screen's alert row, jump to the session pane instead |
| click | select a row and open it (a click inside an already-open inspector keeps your scroll position) |
| `⇥` | jump to the session pane, the only place a permission request can be answered (needs a terminal from [TERMINALS.md](TERMINALS.md)) |
| `esc` / `q` | close the inspector - never kills anything; `q` never quits the pane |
| `f` | follow mode: inspector tracks the most recently active agent |
| `o` | open the selected agent's current file in `$EDITOR` in a new tab (see [TERMINALS.md](TERMINALS.md)) |
| `y` | yank the agent's error, else its failing command, else its activity line; on `main`, the full workstream label |
| `Y` | yank the whole inspector log; on `main` with an empty log, the full workstream label |
| `w` | cycle the layout: fill the pane, wide 64, narrow 44 (persisted) |
| `a` | returned agents: the list at the foot of the tree (default) ↔ full rows on it |
| `s` | the suppressed agents: open the census of everything the platform announced that never did anything (the footer's `n quiet`) |
| `1`-`9` | switch attached session (idle screen, only when unpinned and more than one session is live; no-op otherwise) |
| `t` | cycle the right column: since-last-event, token burn rate (persisted) |
| `⇞` / `⇟` | page the inspector when it is open |
| `esc` (on the digest) | dismiss the away-digest change marks |
| `?` | key help overlay, dropped-event count, source name |
| `ctrl-c` | quit agentpane; agents keep running (asks to confirm when agents are live) |
| `p` / `.` | demo only: pause/resume, step one event |

There is no kill key. Pressing `x` explains why:
`read-only — subagents cannot be killed`.
