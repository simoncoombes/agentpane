package main

// panerun_windows.go — how a TUI invocation is spelled for a Windows terminal.
//
// There is no shell on this path, deliberately. Both Windows backends
// (Windows Terminal and WezTerm) exec a program directly, so the invocation
// travels as ARGV and never has to survive cmd.exe's quoting rules — which
// are not Go's, and which mangle precisely the case that matters here: a
// program path containing a space. `cmd /c "C:\Program Files\x.exe" --flag`
// is a documented footgun on both sides of that boundary, and the way to not
// have it is to not invoke a shell.
//
// paneRun.line is therefore a rendering for the --explain trace only. Nothing
// executes it.

// paneRunFor builds the invocation that pins a new pane to one session.
func paneRunFor(exe, sessionID string, extraArgs []string) paneRun {
	argv := make([]string, 0, 3+len(extraArgs))
	argv = append(argv, exe)
	if sessionID != "" {
		argv = append(argv, "--session", sessionID)
	}
	argv = append(argv, extraArgs...)
	return paneRun{line: displayLine(argv), argv: argv}
}

// editorRun builds the invocation `o` sends to a new tab.
//
// Unlike the unix side the editor setting is a PROGRAM, not a command line:
// with no shell to split it there is no way to tell the space in `code -w`
// from the one in `C:\Program Files\Editor\ed.exe`, and guessing wrong on the
// second is worse than not supporting the first. docs/TERMINALS.md says so.
func editorRun(editor, path string) paneRun {
	argv := []string{editor, path}
	return paneRun{line: displayLine(argv), argv: argv}
}
