# iTerm2 setup

Goal: a window that opens with Claude Code in a large left pane and
`agentpane` in a 64-column right pane, automatically, every time.

iTerm2 is one of four terminals agentpane can split from a hook; tmux, WezTerm
and kitty work too, and [TERMINALS.md](TERMINALS.md) covers those. This page is
the full iTerm2 walkthrough.

There are two routes:

- **Autopane (recommended).** `agentpane install --autopane` adds a `SessionStart`
  hook that tells iTerm2 to split the pane your Claude session just started
  in and run the TUI in the new pane. It works in any window, tab, or pane —
  no saved geometry, nothing to restore — and it follows you: wherever you
  start `claude`, the monitor appears next to it. Section 2.
- **Window Arrangement (the no-hooks alternative).** If you prefer not to
  wire a hook for this (or want the split to exist before any session
  starts), a saved arrangement captures both panes, which profile each runs,
  and the exact divider position, and restores it in one action. Section 3.

Both routes use the same two Dynamic Profiles below. A profile alone cannot
do the job either way: iTerm2 has no split-on-open setting, so profiles by
themselves always leave you one manual split short — the hook or the
arrangement is what closes that gap.

## 1. The two profiles (Dynamic Profiles)

Save this as
`~/Library/Application Support/iTerm2/DynamicProfiles/agentpane.json`.
iTerm2 picks up dynamic profiles within seconds, no restart:

```json
{
  "Profiles": [
    {
      "Name": "Claude Code",
      "Guid": "agentpane-claude-code",
      "Custom Command": "Yes",
      "Command": "/bin/zsh -lc claude"
    },
    {
      "Name": "AgentPane",
      "Guid": "agentpane-monitor",
      "Custom Command": "Yes",
      "Command": "/bin/zsh -lc agentpane",
      "Columns": 64
    }
  ]
}
```

Notes:

- `-l` runs a login shell so `claude` and `agentpane` are found on your
  normal `PATH`.
- `"Columns": 64` only applies when the AgentPane profile opens its own
  window or tab. A split divides the existing pane instead, ignoring it — on
  the autopane route just drag the divider once to taste; on the arrangement
  route the saved arrangement records the real divider position.
- Set each profile's Working Directory (Settings > Profiles > General) to
  your project, or to "Reuse previous session's directory". A hand-started
  `agentpane` attaches to the newest live Claude session whose cwd matches
  its own, so both panes should share a working directory. (Autopane panes
  do not depend on this — they are pinned to a session id; see section 2.)

## 2. Auto-open on session start (recommended)

```sh
agentpane install --autopane
```

That appends one extra `SessionStart` hook group (matcher `startup|resume`)
running `agentpane autopane` — alongside the normal 12 `agentpane hook`
entries, which the same run installs if they are missing. From then on,
starting or resuming an interactive Claude Code session inside an iTerm2
pane splits that pane vertically and runs the TUI in the new pane.

How it behaves:

- **Targeting.** The hook matches the exact pane the session started in (via
  `ITERM_SESSION_ID`), so the split lands next to your session even if you
  have focused another window meanwhile.
- **Pinned to the session.** The new pane runs `agentpane --session <id>`
  with the id from the SessionStart payload, so for its whole life it
  watches the session in the tab it was split from and no other. It never
  re-attaches, and its idle screen offers no session picker (`1`-`9` do
  nothing there). If the pane opens before the session's registry row lands,
  it waits, showing `waiting for session <id>` rather than attaching to a
  neighbour.

  This is a deliberate deviation from SPEC §3.8a, which attaches by "newest
  live session whose cwd matches" and always lists the alternatives. With
  five Claude sessions running in one directory (normal here) that rule is a
  coin flip, and a monitor watching the wrong session looks entirely correct
  while being wrong, which is the worst failure this tool has. The picker is
  still there for a hand-started `agentpane` in a busy directory, where it
  is a recovery path rather than a trap.
- **Profile.** The new pane uses the profile named by `AGENTPANE_PROFILE`
  (default `AgentPane`, from section 1). If that profile does not exist, it
  falls back to your session's own profile, so the pane opens regardless.
- **Guards.** It never opens a pane for: non-iTerm2 terminals, sessions
  nested inside another Claude session, fully headless runs (no controlling
  terminal), `clear`/`compact`/`fork` restarts, a session that already has a
  TUI attached, or a duplicate SessionStart racing the first. All skips are
  silent and exit 0. One honest gap: `claude -p` typed into a terminal still
  has a controlling terminal, so it is not auto-skipped — use the kill
  switch around scripted `-p` loops.
- **Kill switch.** `export AGENTPANE_AUTOPANE=0` disables auto-open without
  uninstalling anything.
- **Uninstall.** `agentpane install --uninstall --autopane` removes just this
  entry; plain `agentpane install --uninstall` removes it along with the hook
  entries. `agentpane doctor` shows whether autopane is installed.

## 3. The Window Arrangement (the no-hooks alternative)

Prefer the split to exist before any session starts, or don't want a
SessionStart hook opening panes? Create the split once, save it as an
arrangement, and restore it on demand:

1. Open a window with the Claude Code profile (Profiles menu > Claude Code).
2. Split it vertically with the AgentPane profile: Shell > Split
   Vertically... and pick AgentPane (or right-click the session > Split Pane
   Vertically with Profile). The right pane starts `agentpane` immediately.
3. Drag the divider until the right pane is 64 columns wide. iTerm2 shows a
   size overlay while you drag. agentpane's wide layout is exactly 64
   columns; if the header rule wraps, the pane is too narrow (`w` inside
   agentpane toggles the 44-column narrow layout if you prefer a slimmer
   pane).
4. Window > Save Window Arrangement. Name it, for example, `claude-code`.
5. Make it open automatically: in Settings > Arrangements select it and set
   it as the default, then in Settings > General > Startup choose "Open
   Default Window Arrangement".
   The arrangement's pane runs a bare `agentpane`, so it is NOT pinned: it
   attaches by the cwd rule, picking the newest live session for its
   directory, and its idle screen lists the others with `1`-`9` to switch.
   If you routinely run several sessions in one directory, prefer autopane
   (section 2), or start the pane by hand with `agentpane --session <id>`.
6. Or bind a key instead: Settings > Keys > Key Bindings > `+`, action
   "Restore Window Arrangement", pick `claude-code`. One keystroke rebuilds
   the split whenever you want it rather than at launch.

## 4. Install the hook

`agentpane hook` is how the TUI sees events. `agentpane install` (section 2)
wires all of this for you; this section is the manual route. Paste this into
the `"hooks"` section of `~/.claude/settings.json` (it is exactly what
`agentpane doctor` prints when the hooks are missing). If you already have
hooks, append the `agentpane hook` entry to each event's existing array; do
not replace what is there. Hooks are re-read per invocation, so running
sessions pick this up without a restart:

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

## 5. Verify the title and badge escapes

When an agent blocks on a permission prompt, agentpane sets the pane title
(OSC 1) and the iTerm2 badge (OSC 1337 SetBadgeFormat) to
`⚑ <agent> needs you`, so the alert reads even with the window unfocused or
the pane collapsed, and clears both on resolution. Verify your terminal
honors these before relying on them. Paste each line into the pane agentpane
will run in:

Title:

```sh
printf '\033]1;agentpane-title-test\007'
```

The tab title changes to `agentpane-title-test`. To see it per pane, enable
per-pane title bars (Settings > Appearance > Panes). Reset it by opening a
new shell prompt or with `printf '\033]1;\007'`.

Badge:

```sh
printf '\033]1337;SetBadgeFormat=%s\007' "$(printf 'needs you' | base64)"
```

Large translucent "needs you" text appears in the pane's top-right corner.
Clear it:

```sh
printf '\033]1337;SetBadgeFormat=\007'
```

Optional, the level-4 escalation path (a system notification for an ask
unanswered past `notify_after`):

```sh
printf '\033]9;agentpane-notify-test\007'
```

macOS should show a notification from iTerm2; if not, allow iTerm2 in System
Settings > Notifications. If any of these tests fails inside tmux, that is
tmux swallowing the escape, not iTerm2; run the test outside tmux to isolate.

## 6. Verify with the demo

In the right pane, stop agentpane (`ctrl-c`) and run:

```sh
agentpane --demo
```

This replays the SPEC §2.10 scenario. You should see, top to bottom:

- header `AGENTS 8` with `⚑ 2 NEEDS YOU` on the right
- the `⚑ NEEDS YOU` band: `fix-ts2345-fallout  permission ×3` blocked 52s on
  `rm -rf node_modules/.cache && pnpm rebuild`, with `+2 retry agents folded`,
  and `typecheck-works…  stalled` at 2m50s
- `main  refactor auth` with its todo `write the migration`
- `fix-ts2345-fallout` with a dim inferred `↺2`
- `typecheck-works…` with a `·······` flatline (nothing running, nothing
  happening)
- `run-auth-tests` showing an open call: `◐ in Bash  3m12s`
- `audit-users-schema` and `write-email-index` live with sparklines
- `document-constr…  queued`, and `lint-and-format` decayed to one dim
  `returned` line
- `reviewer` as a `◇` teammate row with `limited telemetry`

`p` pauses the replay, `.` steps one event at a time, and `--demo-seek <sec>`
jumps to any point, so every state is reachable for a look. If the tree's
trunk renders as boxes or double-width cells, your font lacks the box-drawing
glyphs; check the glyph sample in `agentpane doctor`.

## 7. `agentpane doctor`, line by line

Real output from this machine (run without hooks installed, from a script
rather than a terminal, so the tty lines show their fallback form):

```
agentpane dev doctor

✔ go: go1.26.5 (darwin/arm64)
✔ config: /Users/alex/.config/agentpane/config.toml (defaults apply for missing keys)
⚠ tty: none (open /dev/tty: device not configured) — palette probe skipped; run doctor from the terminal agentpane will live in
⚠ accent: palette query unanswered (200ms timeout); defaulting to accent 6 (cyan) — safe on grey-on-dark and green-on-black alike
✔ live/primary: live=ANSI 6, primary=default fg (SGR 39) — distinct pair
✖ hooks: no 'agentpane hook' entry in /Users/alex/.claude/settings.json — paste this into its "hooks" section:

[ ...the settings.json snippet, reproduced in full in section 4 above... ]

  competing settings — read-only scan of /Users/alex/.claude/settings.json:
⚠ /Users/alex/.claude/settings.json is a symlink -> /Users/alex/dev/dotfiles/settings.json
    these settings look managed by another system (a dotfiles repo or fleet-style installer)
    an installer edit will land in /Users/alex/dev/dotfiles/settings.json; that system's installer may overwrite it,
    or the change may show up as an uncommitted diff in its repo -- consider committing it there too
⚠ competing subagent visualizer on SubagentStart: bash ~/.claude/scripts/it2-pane-hook.sh start
    running two subagent UIs doubles panes and notifications; consider disabling one
    env vars AGENT_PANE_BADGE, AGENT_PANE_CLOSE, AGENT_PANE_CONCISE, AGENT_PANE_LINGER, AGENT_PANE_NOTIFY, AGENT_PANE_OFF, AGENT_PANE_PROFILE in this settings file suggest that tool has its own on/off switch there
⚠ competing subagent visualizer on SubagentStop: bash ~/.claude/scripts/it2-pane-hook.sh stop
    running two subagent UIs doubles panes and notifications; consider disabling one
    env vars AGENT_PANE_BADGE, AGENT_PANE_CLOSE, AGENT_PANE_CONCISE, AGENT_PANE_LINGER, AGENT_PANE_NOTIFY, AGENT_PANE_OFF, AGENT_PANE_PROFILE in this settings file suggest that tool has its own on/off switch there
⚠ env: AGENT_PANE_BADGE, AGENT_PANE_CLOSE, AGENT_PANE_CONCISE, AGENT_PANE_LINGER, AGENT_PANE_NOTIFY, AGENT_PANE_OFF, AGENT_PANE_PROFILE set in the settings env block — that tool may have its own on/off switch there
✔ agentpane entries: none in /Users/alex/.claude/settings.json — the hooks line above prints the snippet to paste
✔ autopane: auto-open on session start: not installed — `agentpane install --autopane`

✔ registry: /Users/alex/.claude/sessions readable, 4 live session(s)
⚠ attach target (no --session): none for ~/Dev/agentpane — an unpinned agentpane would fall back to 3e30 (~/Dev)
⚠ socket: /var/folders/97/yrvtwh7574n952k_z9dlyh400000gn/T/agentpane-3e30d225-2fb5-4c3b-b03f-e69f50dd19f0.sock absent — normal unless an agentpane TUI is attached (the TUI creates it)
✔ transcripts: /Users/alex/.claude/projects/-Users-alex-Dev-agentpane exists
✔ state dir: /Users/alex/.local/state/agentpane writable (current-<session>.json, runs/s-<session>/)
✔ run store: /Users/alex/.local/state/agentpane/runs writable
✔ spark: "tokens" configured — an agent with no token telemetry falls back to calls at render time; the fallback is visible on the row, not from this script (§3.4)

  glyph sample — every glyph below must render one cell wide, no boxes:
  ● ⚑ ○ ◇ ◌ ▪▫· ▁▂▃▄▅▆▇ ◐ ↺ ▸ ▏ ╰─╮ ⠹

⚠ focus reporting (CSI ?1004): cannot verify from a script — check via the pane title test in docs/ITERM2.md
⚠ OSC 8 hyperlinks: cannot verify from a script — ⌘-click a path in the pane to confirm
⚠ OSC 52 clipboard: cannot verify from a script — press y in the pane and check the clipboard (needs iTerm2's clipboard-access preference)
```

Line by line:

- `agentpane dev doctor` - the header names the binary version (`dev` unless
  built with a release version). Doctor is strictly read-only: it never edits
  settings, sockets, or state, and it always exits 0.
- `✔ go:` - the Go runtime the binary was built with, plus OS and
  architecture.
- `✔ config:` - the config path it loaded (`$XDG_CONFIG_HOME` honored). A
  missing file is fine; every key has a default. Invalid keys turn this into
  a `⚠` naming the first problem.
- `⚠ tty:` - doctor could not open `/dev/tty` because it ran from a script,
  so the palette probe was skipped. Run doctor inside the actual pane and
  this line instead reports whether the terminal answered the OSC 4 palette
  query and whether the default foreground is known.
- `⚠ accent:` - the live-accent auto-pick reasoning. With no palette answer
  within 200ms it defaults to ANSI 6 (cyan). When the terminal answers, this
  line explains which index it picked and why: it prefers ANSI 2, staying
  inside your own hue, whenever that is distinguishable from your foreground.
  A forced `--accent` or config value reads `forced by config/flag`.
- `✔ live/primary:` - the resolved pair the pane will use: the live accent
  index versus primary text (your terminal's default foreground, SGR 39).
  They must be distinct; if no distinguishable index exists, live falls back
  to dim-versus-normal weight and this line becomes a warning saying so.
- `✖ hooks:` - no `agentpane hook` entry exists in `~/.claude/settings.json`,
  and the exact snippet to paste follows. After installing, this line becomes
  `✔ hooks: 'agentpane hook' registered for all 12 events`; a partial install
  lists exactly which events are missing.
- `competing settings` - a read-only scan of `~/.claude/settings.json`,
  running the same inspection the installer performs before it writes: same
  detection patterns, essentially the same copy (a parity test,
  `TestInstallShParity`, stops the two from drifting). Doctor never writes to
  the file.
- `⚠ ... is a symlink ->` - the settings file is a symlink into a
  dotfiles repo, so another system manages it. Any
  edit - the installer's included - lands in the target, where that system's
  installer may overwrite it or surface it as an uncommitted diff.
- `⚠ competing subagent visualizer on SubagentStart/SubagentStop:` - one line
  per hooked event, naming the exact command. This machine runs
  `it2-pane-hook.sh`, which opens its own iTerm2 pane per subagent; two
  subagent UIs double panes and notifications, so disable one. The detail line
  lists the `AGENT_PANE_*` env vars that look like that tool's own on/off
  switches.
- `⚠ env:` - the same pane-tool env keys flagged standalone: even without a
  matching hook command, `AGENT_PANE_*` (or any `*_PANE_*`) keys in the
  settings `env` block suggest another pane tool is configured here.
- `✔ agentpane entries:` - doctor's richer view of agentpane's own hook
  entries. All 12 events covered is a `✔`; partial coverage lists the missing
  events and suggests `agentpane install`; a command whose binary path no longer
  exists (or is not the running binary), duplicate entries on one event, and
  an agentpane entry that is not last ahead of an exit-2-style guard each get
  their own line. Here no entries exist yet, so it points back at the
  `✖ hooks` line. Other findings this section can raise, not present on this
  machine: `existing alerting hook on Notification/Stop` (osascript/say/bell
  and friends double up with agentpane's ring/badge - see `--no-bell` /
  `--no-badge` / `--no-notify`), a `statusLine` note (`--oneline` can replace
  or double it), a `⚠` for a duplicated JSON key (the scan, like Claude Code,
  reads the last value; the installer refuses such a file), and `✖` lines for
  malformed JSON, a non-list hooks event, or a settings symlink whose target
  is missing (reported, never touched, and doctor still exits 0).
- `✔ autopane:` - whether the auto-open-on-session-start entry (section 2)
  is installed on `SessionStart`. Installed shows the exact command; not
  installed points at `agentpane install --autopane`. Informational either way —
  auto-open is optional.
- `✔ registry:` - `~/.claude/sessions` is readable and holds this many live
  Claude Code sessions (liveness is checked against the pid, since stale
  session files persist).
- `⚠ attach target (no --session):` - no live session's cwd matches the
  directory doctor ran from, so it names the fallback session an UNPINNED
  agentpane would attach to instead. Run from your project directory with a
  live session there and this is a `✔` naming the newest matching session.
  Either way it describes a hand-run `agentpane`, not an auto-opened pane:
  those are pinned. Pass `agentpane doctor --session <id>` (the id is in
  `$CLAUDE_CODE_SESSION_ID`) to diagnose one pinned pane instead - that form
  resolves the id exactly, and says so plainly when the session is not in the
  registry, because a pinned pane waits for its own id rather than falling
  back to a neighbour.
- `⚠ socket:` - the per-session unix socket the TUI listens on
  (`$TMPDIR/agentpane-<session>.sock`). Absent is normal whenever no
  agentpane TUI is currently attached; the TUI creates it. A socket that
  exists but refuses connections is flagged as probably stale. With
  `--session` the socket checked is that session's, even when the session
  itself is not running.
- `✔ transcripts:` - the transcript directory for this cwd exists, so the
  transcript fallback source has something to tail.
- `✔ state dir:` - `~/.local/state/agentpane` is writable. `--oneline`
  (`current-<session-id>.json`, one per running pane) and run history need it.
- `✔ run store:` - the `runs/` subdirectory is writable; finished runs
  persist here as JSONL, under a `s-<session-id>/` subdirectory per session,
  so one session's history is neither shown in nor pruned by another's pane.
- `✔ spark:` - which sparkline metric the config selects. An individual agent
  with no token telemetry falls back to plotting call cadence at render time;
  that fallback shows on the row itself, not in doctor.
- glyph sample - every glyph agentpane ever draws. Each must render exactly
  one cell wide with no tofu boxes; if not, pick a font with box-drawing and
  block-element coverage (no Nerd Font needed).
- the last three `⚠` lines - capabilities a one-shot script cannot probe:
  focus reporting (powers the away-digest), OSC 8 hyperlinks (⌘-click on
  paths), and OSC 52 clipboard (`y`/`Y`). Each names its manual check;
  section 5 above covers the title/badge escapes the same way.
