package main

// pane.go — the terminal backends agentpane drives.
//
// Three things in this program ask a terminal to do something rather than
// merely writing escapes at it: `agentpane autopane` splits the session's pane
// and runs the TUI in the new one, ⇥ moves focus back to the session pane, and
// `o` opens a file in $EDITOR somewhere that is not the agentpane pane. All
// three were iTerm2-and-AppleScript only, which made every one of them a macOS
// feature by accident rather than by decision.
//
// Each backend is a pure function from the environment to a command chain, so
// the exact argv is unit-testable on a machine that has none of these
// terminals installed (pane_test.go). Running the chain is the only impure
// part, and it lives in runPaneChain.
//
// Detection order matters and is not the obvious one: a MULTIPLEXER wins over
// the terminal hosting it. A Claude session inside tmux inside iTerm2 lives in
// a tmux pane; asking iTerm2 to split would divide the whole tmux viewport and
// leave the new pane outside the multiplexer entirely, next to a tmux window
// rather than next to the session.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The supported backends. backendNone means "this terminal cannot be driven",
// which is a normal answer, not a failure: the TUI itself needs none of this,
// and every caller degrades to doing nothing.
const (
	backendNone    = "none"
	backendITerm2  = "iterm2"
	backendTmux    = "tmux"
	backendWezTerm = "wezterm"
	backendKitty   = "kitty"
)

// paneBackends are the names detectBackend can return, in the order they are
// tried, for the messages that have to list them.
var paneBackends = []string{backendITerm2, backendTmux, backendWezTerm, backendKitty}

// envLookup is os.Getenv, injected so the detector is testable.
type envLookup func(string) string

// detectBackend names the terminal this process can drive.
//
// AGENTPANE_TERMINAL overrides everything, including with "none" — the escape
// hatch for a terminal that is detected correctly and behaves badly, and for
// anyone who wants the pane opened by hand.
func detectBackend(env envLookup) string {
	if forced := strings.TrimSpace(env("AGENTPANE_TERMINAL")); forced != "" {
		for _, b := range append([]string{backendNone}, paneBackends...) {
			if strings.EqualFold(forced, b) {
				return b
			}
		}
		// An unrecognised override is not silently ignored — it becomes
		// "none", so a typo disables auto-open loudly (doctor and --explain
		// both print the detected backend) instead of quietly falling through
		// to a terminal the user was trying to override.
		return backendNone
	}
	// The multiplexer first: see the file comment.
	if env("TMUX") != "" && env("TMUX_PANE") != "" {
		return backendTmux
	}
	if env("TERM_PROGRAM") == "iTerm.app" && env("ITERM_SESSION_ID") != "" {
		return backendITerm2
	}
	if env("WEZTERM_PANE") != "" {
		return backendWezTerm
	}
	if env("KITTY_WINDOW_ID") != "" {
		return backendKitty
	}
	return backendNone
}

// backendReason explains a backendNone verdict in the terms the user can act
// on: what was looked at, and what would have satisfied it.
func backendReason(env envLookup) string {
	if forced := strings.TrimSpace(env("AGENTPANE_TERMINAL")); forced != "" {
		if strings.EqualFold(forced, backendNone) {
			return "AGENTPANE_TERMINAL=none — turned off deliberately"
		}
		return fmt.Sprintf("AGENTPANE_TERMINAL=%q is not a backend name (want one of: none, %s)",
			forced, strings.Join(paneBackends, ", "))
	}
	return fmt.Sprintf("TERM_PROGRAM=%q TERM=%q, and none of $TMUX/$WEZTERM_PANE/$KITTY_WINDOW_ID is set",
		env("TERM_PROGRAM"), env("TERM"))
}

// iTermPaneUUID is the session id from ITERM_SESSION_ID with iTerm2's
// "wNtNpN:" position prefix stripped — the AppleScript matches the uuid half
// against `id of session`. Same technique as it2-pane-hook.sh's
// `${ITERM_SESSION_ID##*:}`.
func iTermPaneUUID(env envLookup) string {
	id := env("ITERM_SESSION_ID")
	if i := strings.LastIndex(id, ":"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// paneProgram resolves a command name to the binary that actually runs it,
// honouring an AGENTPANE_<NAME> override: AGENTPANE_OSASCRIPT, AGENTPANE_TMUX,
// AGENTPANE_WEZTERM, AGENTPANE_KITTY, AGENTPANE_KITTEN.
//
// AGENTPANE_OSASCRIPT has always been the seam the autopane tests use to watch
// a split without opening a real pane; every backend now has the same one, so
// each is testable on a machine with none of these terminals installed. It
// doubles as the way to point at a wrapper or a non-PATH install.
func paneProgram(env envLookup, name string) string {
	if v := strings.TrimSpace(env("AGENTPANE_" + strings.ToUpper(name))); v != "" {
		return v
	}
	return name
}

// paneArgv builds one command with the program-name override applied.
func paneArgv(env envLookup, name string, args ...string) []string {
	return append([]string{paneProgram(env, name)}, args...)
}

// paneCmd is one attempt at a pane operation: a command, plus follow-ups that
// run only when it succeeded and whose own failure does not fail the attempt.
//
// The follow-up exists for WezTerm, whose split always takes focus, so the
// focus has to be handed straight back. If that hand-back fails you still have
// the pane you asked for — just with the cursor in it — which is a far better
// outcome than treating the whole split as failed and retrying it.
type paneCmd struct {
	argv  []string
	after [][]string
}

// splitCommands is the command chain that splits the current pane vertically,
// runs cmdline in the new pane at about cols columns wide, and leaves focus
// where it was. Entries are alternatives, tried in order until one succeeds;
// nil means this backend cannot split from here.
//
// cmdline is already a complete `/bin/sh -c '…'` string (paneCommand), so
// every backend hands it to a shell the same way and none of them has to
// re-quote it.
func splitCommands(backend string, env envLookup, cmdline string, cols int) []paneCmd {
	switch backend {
	case backendITerm2:
		uuid := iTermPaneUUID(env)
		if uuid == "" {
			return nil
		}
		return []paneCmd{{argv: paneArgv(env, "osascript", "-e", autopaneScript(uuid, autopaneProfile(), cmdline))}}

	case backendTmux:
		target := env("TMUX_PANE")
		if target == "" {
			return nil
		}
		// -h splits left/right (tmux calls the DIVIDER horizontal, the
		// opposite of iTerm2's "split vertically" for the same result).
		// -d leaves focus on the session pane, which is the whole point: the
		// pane you were typing in is the pane you should still be typing in.
		// -l <cols> sizes the new pane directly, so unlike iTerm2 there is no
		// resize-and-verify dance afterwards. A cell count for -l needs tmux
		// 3.1; older tmux rejects the flag and splits nothing, so the second
		// alternative drops the sizing and takes tmux's own 50/50 split, which
		// is a pane you can drag rather than no pane at all.
		return []paneCmd{
			{argv: paneArgv(env, "tmux",
				"split-window", "-h", "-d",
				"-l", strconv.Itoa(cols),
				"-t", target,
				cmdline,
			)},
			{argv: paneArgv(env, "tmux",
				"split-window", "-h", "-d",
				"-t", target,
				cmdline,
			)},
		}

	case backendWezTerm:
		target := env("WEZTERM_PANE")
		if target == "" {
			return nil
		}
		// wezterm has no "split without focusing", so focus is taken and
		// handed straight back as a follow-up.
		return []paneCmd{{
			argv: paneArgv(env, "wezterm",
				"cli", "split-pane",
				"--pane-id", target,
				"--right", "--cells", strconv.Itoa(cols),
				"--", "/bin/sh", "-c", cmdline,
			),
			after: [][]string{paneArgv(env, "wezterm", "cli", "activate-pane", "--pane-id", target)},
		}}

	case backendKitty:
		// kitty's remote control is off by default (allow_remote_control in
		// kitty.conf), and the launcher was renamed from `kitty @` to
		// `kitten @` in 0.29 — hence two alternatives.
		args := []string{
			"@", "launch",
			"--type=window", "--location=vsplit",
			"--cwd=current", "--dont-take-focus",
			"/bin/sh", "-c", cmdline,
		}
		return []paneCmd{
			{argv: paneArgv(env, "kitten", args...)},
			{argv: paneArgv(env, "kitty", args...)},
		}
	}
	return nil
}

// focusCommands moves focus from the agentpane pane to the session pane — the
// pane you type in, and the only place a permission request can be answered.
//
// Every backend aims LEFT rather than at a remembered pane id: agentpane is
// split to the right of the session by construction (splitCommands above, and
// the manual setups in docs/ITERM2.md), and the TUI is a separate process that
// never saw the id of the pane it was split from.
func focusCommands(backend string, env envLookup) []paneCmd {
	switch backend {
	case backendITerm2:
		return []paneCmd{{argv: paneArgv(env, "osascript", "-e",
			`tell application "iTerm2" to select first session of current tab of current window`)}}

	case backendTmux:
		// {left-of} is the pane sharing our left edge, which is the session
		// pane in the standard layout. A stacked or otherwise unusual layout
		// has no left neighbour and the command fails, so `-l` (the
		// last-active pane) is the fallback: you got here from the session
		// pane, so that is almost always the same pane.
		return []paneCmd{
			{argv: paneArgv(env, "tmux", "select-pane", "-t", "{left-of}")},
			{argv: paneArgv(env, "tmux", "select-pane", "-l")},
		}

	case backendWezTerm:
		return []paneCmd{{argv: paneArgv(env, "wezterm", "cli", "activate-pane-direction", "--direction", "Left")}}

	case backendKitty:
		return []paneCmd{
			{argv: paneArgv(env, "kitten", "@", "focus-window", "--match", "neighbor:left")},
			{argv: paneArgv(env, "kitty", "@", "focus-window", "--match", "neighbor:left")},
		}
	}
	return nil
}

// tabCommands opens cmdline in a new tab or window — where `o` sends $EDITOR,
// so a file never opens over the tree. nil means the backend cannot, and
// openInEditor falls back to the desktop opener.
func tabCommands(backend string, env envLookup, cmdline string) []paneCmd {
	switch backend {
	case backendITerm2:
		return []paneCmd{{argv: paneArgv(env, "osascript", "-e", iTermTabScript(cmdline))}}
	case backendTmux:
		return []paneCmd{{argv: paneArgv(env, "tmux", "new-window", cmdline)}}
	case backendWezTerm:
		return []paneCmd{{argv: paneArgv(env, "wezterm", "cli", "spawn", "--", "/bin/sh", "-c", cmdline)}}
	case backendKitty:
		args := []string{"@", "launch", "--type=tab", "--cwd=current", "/bin/sh", "-c", cmdline}
		return []paneCmd{
			{argv: paneArgv(env, "kitten", args...)},
			{argv: paneArgv(env, "kitty", args...)},
		}
	}
	return nil
}

// runPaneChain runs the alternatives in order and reports whether one worked.
//
// "Worked" is exit 0 within timeout, or still running at timeout. The second
// case is not optimism: osascript against a busy iTerm2 can outlast any bound
// worth waiting for, and the pane it was asked to create gets created anyway.
// A timed-out process is ABANDONED, never killed — no code path in agentpane
// may signal a process (§5.3, PART 10) — and its Wait goroutine reaps it.
//
// A command that is not installed (kitten on a machine with only kitty) or
// exits non-zero (kitty's remote control disabled) falls through to the next
// alternative, and an exhausted chain returns false so the caller can release
// its lock and stay quiet.
func runPaneChain(chain []paneCmd, timeout time.Duration) bool {
	for _, c := range chain {
		if !runPaneOnce(c.argv, timeout) {
			continue
		}
		for _, follow := range c.after {
			runPaneOnce(follow, timeout) // best effort; see paneCmd
		}
		return true
	}
	return false
}

// runPaneOnce runs one command under the abandon-on-timeout rule.
func runPaneOnce(argv []string, timeout time.Duration) bool {
	if len(argv) == 0 {
		return false
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(timeout):
		return true // still going; abandon it and assume it lands
	}
}

// desktopOpenCommands is the last resort for `o`: hand the file to whatever
// the desktop uses. It opens somewhere other than this pane by definition, so
// it satisfies the rule even though it cannot honour $EDITOR on every path.
func desktopOpenCommands(editor, path string) []paneCmd {
	if runtime.GOOS == "darwin" {
		return []paneCmd{
			{argv: []string{"open", "-a", editor, path}},
			{argv: []string{"open", path}},
		}
	}
	return []paneCmd{
		{argv: []string{"xdg-open", path}},
	}
}

// currentBackend is detectBackend against the real environment, as a var so
// tests and `doctor` share one entry point.
var currentBackend = func() string { return detectBackend(os.Getenv) }
