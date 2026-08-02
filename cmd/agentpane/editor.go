package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// osascriptTimeout bounds every AppleScript call so a hung iTerm2 can never
// wedge a background helper (the UI thread never waits on these at all).
const osascriptTimeout = 5 * time.Second

// openInEditor returns the `o` handler (§5.3): open the agent's current file
// in $EDITOR in a NEW iTerm2 tab — never in the agentpane pane itself. The
// work happens in a goroutine so the UI never blocks; failures degrade to
// `open -a <editor>`, then `open <path>`, and are only ever logged.
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
			if _, err := exec.LookPath("osascript"); err == nil {
				script := iTermTabScript(editor, path)
				if err := runOsascript(script); err == nil {
					return
				}
				dbg.printf("o: iTerm2 tab failed, falling back to open -a")
			}
			// Degraded path: hand the file to the editor app, then to the
			// system default.
			if err := runQuick("open", "-a", editor, path); err == nil {
				return
			}
			if err := runQuick("open", path); err != nil {
				dbg.printf("o: every open path failed for %s", path)
			}
		}()
		return nil
	}
}

// iTermTabScript builds the AppleScript for a new tab running the editor.
func iTermTabScript(editor, path string) string {
	cmd := editor + " " + shellQuote(path)
	return `tell application "iTerm2"
	tell current window
		create tab with default profile command "` + appleScriptEscape("/bin/sh -c "+shellQuote(cmd)) + `"
	end tell
end tell`
}

// focusLeftPane returns the ⏎-on-band handler (§5.3): jump focus to the
// session pane via the iTerm2 AppleScript interface, fire-and-forget.
func focusLeftPane(dbg *debugLog) func() error {
	return func() error {
		if _, err := exec.LookPath("osascript"); err != nil {
			return err
		}
		go func() {
			script := `tell application "iTerm2" to select first session of current tab of current window`
			if err := runOsascript(script); err != nil {
				dbg.printf("focus jump failed: %v", err)
			}
		}()
		return nil
	}
}

// runOsascript executes one AppleScript with a hard timeout.
func runOsascript(script string) error {
	return runQuick("osascript", "-e", script)
}

// runQuick runs a command, reporting a timeout after osascriptTimeout. The
// timed-out helper is abandoned, never killed — no code path anywhere may
// signal a process (§5.3, PART 10) — and the Wait goroutine reaps it whenever
// it eventually finishes.
func runQuick(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(osascriptTimeout):
		return fmt.Errorf("%s timed out", name)
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
