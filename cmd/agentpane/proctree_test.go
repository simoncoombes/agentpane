package main

import "testing"

// TestClaudeAbove covers guard (d)'s Windows answer without needing Windows:
// the syscall half only builds a table, and this is the half that decides.
func TestClaudeAbove(t *testing.T) {
	// A nested session's real shape: the outer claude runs a tool shell, the
	// shell runs the inner claude, and the hook fires for the inner one.
	nested := map[int]procRow{
		100: {ppid: 1, exe: "WindowsTerminal.exe"},
		200: {ppid: 100, exe: "claude.exe"}, // the outer session
		300: {ppid: 200, exe: "cmd.exe"},    // its Bash tool
		400: {ppid: 300, exe: "claude.exe"}, // the session the hook fired for
	}
	// A top-level session, same terminal, no claude above it.
	toplevel := map[int]procRow{
		100: {ppid: 1, exe: "WindowsTerminal.exe"},
		400: {ppid: 100, exe: "claude.exe"},
	}

	cases := []struct {
		name  string
		pid   int
		table map[int]procRow
		want  bool
		why   string
	}{
		{"nested session", 400, nested, true, "the outer claude is two hops up"},
		{"top-level session", 400, toplevel, false, "nothing above it but the terminal"},
		{"the session itself does not count", 200, toplevel, false, "pid is excluded from its own ancestry"},
		{"pid missing from the table", 999, nested, false, "an unreadable chain defaults to not-nested"},
		{"empty table", 400, map[int]procRow{}, false, "nothing to walk"},

		// Windows recycles pids and reports parents that have already exited,
		// so a table can describe a loop. It must end the walk, not hang it.
		{"a cycle terminates", 1, map[int]procRow{
			1: {ppid: 2, exe: "a.exe"},
			2: {ppid: 1, exe: "b.exe"},
		}, false, "the seen-set ends the climb"},
		{"self-parenting root terminates", 4, map[int]procRow{
			4: {ppid: 4, exe: "System"},
		}, false, "pid 0/4 are their own parents on Windows"},
	}
	for _, c := range cases {
		if got := claudeAbove(c.pid, c.table); got != c.want {
			t.Errorf("%s: claudeAbove(%d) = %v, want %v (%s)", c.name, c.pid, got, c.want, c.why)
		}
	}
}

// A chain longer than the bound answers false rather than climbing forever.
func TestClaudeAboveDepthBound(t *testing.T) {
	deep := map[int]procRow{}
	for i := 1; i <= maxProcessWalk+10; i++ {
		deep[i] = procRow{ppid: i + 1, exe: "shell.exe"}
	}
	deep[maxProcessWalk+11] = procRow{ppid: 0, exe: "claude.exe"}
	if claudeAbove(1, deep) {
		t.Error("a claude past the depth bound was reported; the walk must give up instead")
	}
}

func TestIsClaudeExe(t *testing.T) {
	yes := []string{"claude.exe", "claude", "CLAUDE.EXE", `C:\Users\x\.local\bin\claude.exe`, "  claude.exe  "}
	no := []string{"", "node.exe", "claude-code.exe", "myclaude.exe", "cmd.exe", "claude.exe.bak"}
	for _, s := range yes {
		if !isClaudeExe(s) {
			t.Errorf("isClaudeExe(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isClaudeExe(s) {
			t.Errorf("isClaudeExe(%q) = true, want false", s)
		}
	}
}
