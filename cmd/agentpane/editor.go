package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// paneCmdTimeout bounds every terminal-driving command — an AppleScript, a
// tmux or wezterm CLI call — so a hung terminal can never wedge a background
// helper. The UI thread never waits on these at all.
const paneCmdTimeout = 5 * time.Second

// openInEditor returns the `o` handler (§5.3): open the agent's current file
// in $EDITOR in a NEW tab — never in the agentpane pane itself. The work
// happens in a goroutine so the UI never blocks; a terminal that cannot open
// a tab degrades to the desktop opener, and failures are only ever logged.
func openInEditor(configEditor string, dbg *debugLog) func(path string) error {
	return func(path string) error {
		if path == "" {
			return fmt.Errorf("no file")
		}
		editor := configEditor
		if editor == "" {
			editor = os.Getenv("EDITOR") // PART 7: editor="" falls back to $EDITOR
		}
		if editor == "" {
			editor = "vi"
		}
		go func() {
			backend := currentBackend()
			if chain := tabCommands(backend, os.Getenv, editorRun(editor, path)); len(chain) > 0 {
				if runPaneChain(chain, paneCmdTimeout) {
					return
				}
				dbg.printf("o: %s tab failed, falling back to the desktop opener", backend)
			}
			// Degraded path: hand the file to the editor app, then to
			// whatever the desktop uses.
			if !runPaneChain(desktopOpenCommands(editor, path), paneCmdTimeout) {
				dbg.printf("o: every open path failed for %s", path)
			}
		}()
		return nil
	}
}

// iTermTabScript builds the AppleScript for a new tab running cmdline, which
// is already a complete `/bin/sh -c '…'` string.
func iTermTabScript(cmdline string) string {
	return `tell application "iTerm2"
	tell current window
		create tab with default profile command "` + appleScriptEscape(cmdline) + `"
	end tell
end tell`
}

// focusLeftPane returns the ⇥ handler (§5.3): jump focus to the session pane,
// fire-and-forget, through whichever terminal backend this pane is running
// under. A terminal agentpane cannot drive returns an error, which the key
// turns into the "no focus jump" toast rather than silence.
func focusLeftPane(dbg *debugLog) func() error {
	return func() error {
		backend := currentBackend()
		chain := focusCommands(backend, os.Getenv)
		if len(chain) == 0 {
			return fmt.Errorf("no focus jump for %s", backend)
		}
		go func() {
			if !runPaneChain(chain, paneCmdTimeout) {
				dbg.printf("focus jump failed under %s", backend)
			}
		}()
		return nil
	}
}

// shellQuote single-quotes s for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptEscape escapes backslashes and double quotes for embedding in an
// AppleScript string literal.
func appleScriptEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
