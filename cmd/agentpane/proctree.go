package main

// proctree.go — walking a process tree, as a pure function.
//
// Only the Windows build reads this today: guard (d) asks "was this claude
// launched from inside another Claude session", and unix answers it by
// reading the process's ENVIRONMENT for CLAUDE_CODE_CHILD_SESSION, which
// Windows will not hand over without a great deal of unsafe code. The
// structural answer — is there a second claude above this one — needs only
// the (pid, ppid, name) table every OS will hand over cheaply.
//
// The walk lives here, untagged, rather than in autopane_windows.go, because
// it is the part that can be wrong in interesting ways (cycles, recycled
// pids, a truncated table) and a test for it should run on every machine
// rather than only on the one platform that calls it.

import (
	"path"
	"strings"
)

// procRow is one process as a snapshot reports it.
type procRow struct {
	ppid int
	exe  string // the image name, with whatever extension the OS reports
}

// maxProcessWalk bounds the climb. A genuine chain is a handful of processes
// deep; anything longer is a table that disagrees with itself, and the answer
// there is "not nested" rather than a hang.
const maxProcessWalk = 32

// claudeAbove reports whether any ANCESTOR of pid is a claude process. pid
// itself is excluded: it is the claude the hook fired for, and the question
// is whether another one contains it.
//
// A pid missing from the table, a root reached, a cycle, or a walk longer
// than maxProcessWalk all answer false — the same default the unix probe
// takes when it cannot read an environment, and for the same reason: a false
// skip disables auto-open, a false open costs one extra pane.
func claudeAbove(pid int, table map[int]procRow) bool {
	seen := map[int]bool{pid: true}
	cur := pid
	for range maxProcessWalk {
		row, ok := table[cur]
		if !ok {
			return false
		}
		parent := row.ppid
		// pid 0 is the idle process and its own parent on Windows; either
		// way a non-positive or already-visited pid is the end of the climb.
		if parent <= 0 || seen[parent] {
			return false
		}
		if isClaudeExe(table[parent].exe) {
			return true
		}
		seen[parent] = true
		cur = parent
	}
	return false
}

// isClaudeExe reports whether an image name is the Claude Code CLI.
//
// The name is the whole test, which is what makes this weaker than reading an
// environment: a Claude Code installed through npm runs as node.exe and is
// invisible here, so a session nested inside THAT one gets its own pane. That
// is the documented failure direction — one pane too many, never none.
//
// Both separators are stripped explicitly rather than by filepath.Base,
// because this compiles on every platform and a backslash is not a separator
// on most of them. Toolhelp reports a bare image name in practice; accepting
// a path costs one line and removes a way for the caller to be surprised.
func isClaudeExe(exe string) bool {
	base := strings.TrimSpace(exe)
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, path.Ext(base))
	return strings.EqualFold(base, "claude")
}
