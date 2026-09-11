# Installing agentpane

The README has the two commands. This is what they do, the auto-open wiring,
and the exact shape of the hook block if you would rather write it yourself.

## Getting the binary

`get.sh` is a download, nothing more. It works out your OS and architecture,
fetches that release archive, verifies it against the `checksums.txt`
published with the release, and installs the binary — by rename, so a pane
that is already running keeps the inode it started with. It does not touch
your Claude Code settings and could not ask you about it if it wanted to: its
own stdin is the pipe you fed it.

```sh
curl -fsSL https://raw.githubusercontent.com/simoncoombes/agentpane/main/get.sh | sh
```

`AGENTPANE_VERSION` pins a tag, `AGENTPANE_BIN_DIR` chooses where it lands
(default `~/.local/bin`). If you would rather see it before you run it, it is
[get.sh](../get.sh) in this repository — 120 lines of POSIX sh.

Releases carry darwin and linux, arm64 and amd64. Anything else builds from
source with `go install`.

## The installer

`agentpane install` wires the hooks into `~/.claude/settings.json` for you.
It is the same program as `./install.sh` in this repository — the script is
embedded in the binary, so a downloaded binary needs no clone and there is no
second implementation to keep correct. Every flag below works either way.

It
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
agentpane install                 # inspect, warn, confirm, write
agentpane install --autopane      # same, plus auto-open the TUI on session start
agentpane install --dry-run       # plan + unified diff, writes nothing
agentpane install --uninstall     # remove only agentpane's entries (hook + autopane)
agentpane install --print-snippet # just the JSON, for manual pasting
```

Other flags: `--yes` (skip the prompt; required when stdin is not a tty),
`--binary PATH` (defaults to the running executable), `--settings PATH`.

Exit codes are the script's: 0 done or nothing to do, 1 you refused, 2 the
environment is wrong.

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

## Auto-open

`agentpane install --autopane` adds one extra `SessionStart` hook group (matcher
`startup|resume`) running `agentpane autopane`. From then on, starting an
interactive Claude Code session splits the pane it started in and runs the
agentpane TUI in the new pane, with no arrangement to restore and no manual
split.

This works in iTerm2, tmux, WezTerm and kitty. [TERMINALS.md](TERMINALS.md)
covers what each one needs and how the terminal is detected;
[ITERM2.md](ITERM2.md) is the longer iTerm2 walkthrough, including the
profiles and the no-hooks alternative.

`agentpane autopane` is silent and exits 0 on every path. It opens a pane
only when ALL of these hold, and skips silently otherwise:

- the payload is a real `SessionStart` with source `startup` or `resume`
  (`clear`/`compact`/`fork` re-fire mid-session and never open panes);
- `AGENTPANE_AUTOPANE` is not `"0"` — the kill switch: `export
  AGENTPANE_AUTOPANE=0` disables auto-open without uninstalling;
- the session runs in a terminal agentpane can split, and one that names the
  pane it is in: iTerm2, tmux, WezTerm or kitty. See
  [TERMINALS.md](TERMINALS.md) for the environment variables each is detected
  by, and for `AGENTPANE_TERMINAL`, which overrides the detection;
- the session is not nested inside another Claude session
  (`CLAUDE_CODE_CHILD_SESSION` unset);
- the `claude` process has a controlling terminal (a fully headless run —
  cron, launchd, CI, a detached pipeline — gets no pane; note that
  `claude -p` typed into a terminal still has one, so use the kill switch
  around scripted `-p` loops);
- no agentpane TUI is already attached to the session (its socket exists);
- a per-session lockfile wasn't just taken by a duplicate event (stale locks
  older than 1h are reclaimed).

The new pane is `AGENTPANE_COLUMNS` wide (default 64, floored at 44, and never
so wide that the session pane drops below 80). On iTerm2 it uses the profile
named by `AGENTPANE_PROFILE` (default `AgentPane`, the one docs/ITERM2.md ships
via Dynamic Profiles) and falls back to the current session's own profile when
that one does not exist.

When nothing happens, `agentpane autopane --explain` runs the same guard chain
against your current environment and prints a verdict per guard, opening
nothing. To see the same trace from a real hook firing, run `agentpane autopane
--debug-on` and read `~/.local/state/agentpane/autopane.log`.

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

Remove just the auto-open entry with `agentpane install --uninstall --autopane`;
plain `agentpane install --uninstall` removes it along with the hook entries.
`agentpane doctor` reports whether autopane is installed.

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
