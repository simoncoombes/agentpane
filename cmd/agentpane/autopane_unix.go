//go:build !windows

package main

// autopane_unix.go — the two autopane guards that have to ask the operating
// system about another process. Both go through ps(1), which is the portable
// way to read facts about a process you did not spawn.

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// platformParentHasTTY backs guard (e): the claude process must have a
// controlling terminal. Empty output, "??" on macOS or "?" on Linux all mean
// none. See parentHasTTY for what this does and does not catch.
func platformParentHasTTY(pid int) bool {
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	tty := strings.TrimSpace(string(out))
	return tty != "" && tty != "??" && tty != "?"
}

// platformParentIsNestedClaude backs guard (d) by reading the claude
// process's ENVIRONMENT: a claude launched from inside another session
// inherits CLAUDE_CODE_CHILD_SESSION from the tool shell that started it.
// See parentIsNestedClaude for why our own environment is the wrong thing to
// look at.
func platformParentIsNestedClaude(pid int) bool {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "CLAUDE_CODE_CHILD_SESSION=")
}

// dialRefused reports whether a dial failure means "the path is there but
// nothing is listening" — a leftover socket file rather than a live TUI.
func dialRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTSOCK)
}
