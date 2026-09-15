//go:build !windows

package main

import (
	"os"
	"testing"
)

// fakeprog_unix_test.go — the observation seam for everything that drives a
// terminal.
//
// None of iTerm2, tmux, WezTerm or kitty is installed on the machine that runs
// these tests, and that is the point: the code turns an environment into an
// exact argv, and a fake program standing in for osascript is how that argv
// gets caught without a terminal in the room.

// writeFakeProgram writes an executable stand-in at path, with body as its
// shell script.
func writeFakeProgram(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// alwaysFails names a program that exists and exits non-zero — the
// "installed, but it failed" rung of a command chain, which is the one that
// has to fall through to the next alternative rather than stopping.
func alwaysFails() string { return "/usr/bin/false" }
