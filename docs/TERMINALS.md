# Terminals

The TUI runs in any terminal. Two features need to drive the terminal itself,
and those are the ones with a list:

- auto-open, where a `SessionStart` hook splits the pane your Claude session
  just started in and runs the TUI in the new pane
- `⇥`, which moves focus back to the session pane so you can answer whoever is
  blocked

Both work in iTerm2, tmux, WezTerm, kitty and Windows Terminal. Everywhere
else the TUI is fine, auto-open does nothing, and `⇥` says so.

## What each terminal needs

| Terminal | Detected by | Needs | Tested |
|---|---|---|---|
| tmux | `$TMUX` and `$TMUX_PANE` | tmux on `PATH`. 3.1 or newer gets a 64-column pane; older tmux gets tmux's own 50/50 split, because `split-window -l` only takes a cell count from 3.1 | argv only |
| iTerm2 | `TERM_PROGRAM=iTerm.app` and `$ITERM_SESSION_ID` | `osascript`, and iTerm2 allowed under System Settings > Privacy & Security > Automation | live |
| WezTerm | `$WEZTERM_PANE` | `wezterm` on `PATH` | argv only |
| kitty | `$KITTY_WINDOW_ID` | `allow_remote_control yes` in `kitty.conf`, plus `kitten` or `kitty` on `PATH` | argv only |
| Windows Terminal | `$WT_SESSION` | `wt.exe` on `PATH`. 1.7 or newer gets a sized pane; older wt gets wt's own 50/50, because `--size` only exists from 1.7. `⇥` and the focus hand-back after a split need 1.13 (`move-focus`) | argv only |

"argv only" means the exact command agentpane builds is covered by tests
(`cmd/agentpane/pane_test.go`) but has not been run against that terminal on a
real machine. If one of them misbehaves, `agentpane autopane --explain` prints
the command it would run, so you can run it yourself and see what the terminal
says.

Ghostty, Alacritty, Terminal.app, ConEmu and VS Code's terminal have no way to
script a split from outside that agentpane drives, so there is no backend for
them. Running Claude inside tmux inside any of them does work, because tmux is
what gets driven.

## Windows

PowerShell in Windows Terminal is the supported setup, and it is the one the
`wt` row above describes. WezTerm on Windows works too and is the better of
the two where you have it — see the caveat below.

One difference is worth knowing before you rely on it. Every other backend
addresses the pane it is splitting BY ID: iTerm2 matches a session uuid, tmux
a `%pane`, WezTerm a `--pane-id`. Windows Terminal has no such addressing —
`wt -w 0 split-pane` splits whatever pane is ACTIVE in the most recently used
window. `SessionStart` fires within milliseconds of the session starting, so
in practice that is the pane you just started Claude in. But if you start a
session and immediately click into another pane of the same window, the split
lands in the pane you clicked into. WezTerm on Windows does not have this
problem, because it kept the pane id.

Two guards also work differently, because the questions they ask do not exist
on Windows in the same form:

- The headless check asks whether a CONSOLE is attached rather than a
  controlling tty. A run under Task Scheduler, a service or CI has none and is
  skipped, which is the case the guard exists for.
- The nested-session check looks for a second `claude` in the process tree
  rather than reading the outer process's environment, which Windows does not
  hand over. A Claude Code installed through npm runs as `node.exe` and is
  invisible to it, so a session nested inside that one gets its own pane. The
  failure direction is one pane too many, never none.

`o` (open in `$EDITOR`) takes a program on Windows, not a command line: with
no shell in the path there is no way to tell the space in `code -w` from the
one in `C:\Program Files\Editor\ed.exe`, and guessing wrong on the second is
worse than not supporting the first.

## Detection order

tmux wins over the terminal hosting it. A session running in tmux inside iTerm2
lives in a tmux pane, and asking iTerm2 to split would divide the whole tmux
viewport and leave the new pane outside tmux, beside a tmux window rather than
beside the session.

After tmux the order is iTerm2, WezTerm, kitty, Windows Terminal. Each is
checked by the environment variable in the table above, so a terminal that does
not set its own variable is not detected even if `TERM` looks right. Windows
Terminal is last on purpose: it is the only one that cannot address the pane it
splits, so anything that can aim should win a tie. Nothing sets both in the
wild — `WT_SESSION` is Windows Terminal's alone — so this only decides a case
somebody constructed deliberately.

The same tmux rule applies on Windows: a WSL tmux hosted by Windows Terminal is
a tmux pane, and `wt` would split the whole terminal around it.

## Overrides

`AGENTPANE_TERMINAL` replaces the detection: `none`, `iterm2`, `tmux`,
`wezterm`, `kitty` or `wt`. `AGENTPANE_TERMINAL=none` turns auto-open and `⇥` off
without uninstalling the hook. A value that is not one of those names also
turns them off rather than falling through to detection, so a typo fails where
you can see it; `agentpane doctor` prints the value it did not recognise.

Each backend's program name can be pointed somewhere else:
`AGENTPANE_OSASCRIPT`, `AGENTPANE_TMUX`, `AGENTPANE_WEZTERM`,
`AGENTPANE_KITTY`, `AGENTPANE_KITTEN`, `AGENTPANE_WT`. Use these for a
non-`PATH` install or a wrapper script.

## Diagnosis

`agentpane doctor` names the detected backend and whether the program that
drives it is on `PATH`.

`agentpane autopane --explain` runs the full guard chain against the current
environment, prints a verdict for each guard, and opens nothing. Guard (c) is
the terminal check; it prints what it looked at when it finds nothing.

To trace a real hook firing rather than a hand-run one, `agentpane autopane
--debug-on` writes the same trace to `~/.local/state/agentpane/autopane.log`
on every session start. `--debug-off` stops it.

## Without a supported terminal

Split the pane yourself and run `agentpane` in it. The TUI is identical: it
reads the same hook socket and transcripts, and the only things missing are the
automatic split and the focus jump. Answer permission prompts by moving to the
session pane however your terminal does that.

The escapes the TUI writes degrade on their own. OSC 8 hyperlinks, OSC 52
clipboard, OSC 9 notifications, the OSC 4/10 palette probe and the pane title
are all ignored by a terminal that does not implement them, and `y` also pipes
to `pbcopy` on macOS in case OSC 52 is disabled. The iTerm2 badge (OSC 1337
`SetBadgeFormat`) is the one output that only ever appears in iTerm2.

Linux builds ship for amd64 and arm64, Windows for amd64 and arm64. `o` (open
in `$EDITOR`) uses the terminal's own new-tab command where there is one, and
falls back to `open` on macOS, `xdg-open` on Linux, and the editor then the
shell association (`start`) on Windows.
