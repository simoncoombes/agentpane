//go:build !windows

package main

// panerun_unix.go — how a TUI invocation is spelled for a unix terminal.
//
// Every backend here ends up at /bin/sh, but by two different routes: iTerm2
// and tmux take a command LINE and hand it to a shell themselves, while
// WezTerm and kitty exec a program, so the shell has to be named. Building
// both spellings once, up front, is what keeps quoting out of the backends —
// see paneRun in pane.go for why the two travel together.

// paneRunFor builds the invocation that pins a new pane to one session.
func paneRunFor(exe, sessionID string, extraArgs []string) paneRun {
	line := paneCommand(exe, sessionID, extraArgs)
	return paneRun{line: line, argv: []string{"/bin/sh", "-c", line}}
}

// editorRun builds the invocation `o` sends to a new tab. The editor setting
// is a command line here, not just a program, so `code -w` works.
func editorRun(editor, path string) paneRun {
	line := "/bin/sh -c " + shellQuote(editor+" "+shellQuote(path))
	return paneRun{line: line, argv: []string{"/bin/sh", "-c", line}}
}
