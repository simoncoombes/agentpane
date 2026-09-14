package main

// autopane_windows.go — the two autopane guards, answered the way Windows
// answers them.
//
// Neither is a straight port. Guard (e) asks about a controlling terminal,
// which Windows does not have; guard (d) asks about another process's
// environment, which Windows will not hand over cheaply. Both settle for a
// near neighbour, and both keep the unix version's bias: when the answer is
// not knowable, say the thing that leaves auto-open working.

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformParentHasTTY backs guard (e): is this a headless run?
//
// pid is ignored, and that is the honest implementation rather than a
// shortcut. Windows has no controlling terminal to ask a process about; it
// has a CONSOLE, which a child inherits from its parent unless it was
// created with DETACHED_PROCESS or its own new console. This hook is a child
// of the claude process, so "am I attached to a console" and "is claude
// attached to a console" are the same question for every case guard (e)
// exists to catch — a run under Task Scheduler, a service, or CI has no
// console anywhere in the chain.
//
// The same limit applies as on unix: `claude -p` typed into a terminal keeps
// that terminal's console, so this does not filter it. AGENTPANE_AUTOPANE=0
// is the switch for that case.
func platformParentHasTTY(pid int) bool {
	return platformConsoleColumns() > 0
}

// platformParentIsNestedClaude backs guard (d) structurally: is there another
// claude ABOVE this one in the process tree? A nested session's chain is
// claude → the tool's shell → claude → this hook, so the outer one shows up
// as an ancestor of pid.
//
// See isClaudeExe for what this misses.
func platformParentIsNestedClaude(pid int) bool {
	table, err := processTable()
	if err != nil {
		return false // unreadable → not nested, per the guard's bias
	}
	return claudeAbove(pid, table)
}

// processTable snapshots every process on the machine as pid → (ppid, image
// name).
//
// Toolhelp is the one process enumeration that needs no privileges and no
// unsafe memory reads: it returns names and parents already, which is exactly
// the table claudeAbove walks. A snapshot is a point in time and processes
// die inside it; that is fine, because a chain that has already disappeared
// cannot be the session this hook fired for.
func processTable() (map[int]procRow, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // nothing to do with a close failure here

	table := make(map[int]procRow, 256)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		table[int(entry.ProcessID)] = procRow{
			ppid: int(entry.ParentProcessID),
			exe:  windows.UTF16ToString(entry.ExeFile[:]),
		}
	}
	return table, nil
}

// dialRefused reports whether a dial failure means "the path is there but
// nothing is listening".
//
// Windows answers a refused AF_UNIX connect with the Winsock error, not the
// POSIX one. syscall.ECONNREFUSED exists on Windows but is a synthetic
// APPLICATION_ERROR value nothing ever returns, so comparing against it would
// classify every leftover socket file as a live TUI — and guard (f) would then
// skip auto-open for that session id forever, including across --resume.
func dialRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) ||
		errors.Is(err, windows.WSAENOTSOCK) ||
		errors.Is(err, windows.WSAECONNRESET)
}
