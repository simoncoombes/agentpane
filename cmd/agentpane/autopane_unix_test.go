//go:build !windows

package main

// autopane_unix_test.go — the two guard probes that can only be exercised
// where they are implemented. Both drive the real ps(1) path in
// autopane_unix.go against live fixtures, and the setsid trick that makes the
// headless case deterministic has no Windows equivalent. The Windows halves
// are covered by TestClaudeAbove, which tests the part of them that can
// actually be wrong.

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestParentHasTTY probes the REAL ps(1) implementation. The deterministic
// half: a setsid-detached process has no controlling terminal — the exact
// state of a fully headless run (cron, launchd, CI, `claude -p` in a
// detached pipeline) — and must report false. The positive half only runs
// when the test process itself has a controlling terminal to inherit
// (running `go test` from a real terminal); under CI it is skipped and the
// guard's accept side is covered by the stubbed pass-path tests above.
//
// Deliberately NOT verified here: `claude -p` typed into an interactive
// terminal KEEPS that terminal as its controlling tty, so guard (e) does not
// filter it (see the parentHasTTY doc comment) — flagged for the live check
// after install.
func TestParentHasTTY(t *testing.T) {
	// Bogus pid: ps fails → false, never a panic.
	if parentHasTTY(1<<30 + 7) {
		t.Fatal("nonexistent pid reported a tty")
	}

	detached := exec.Command("/bin/sleep", "30")
	detached.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := detached.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		detached.Process.Kill() //nolint:errcheck // test fixture cleanup only
		detached.Wait()         //nolint:errcheck
	}()
	if parentHasTTY(detached.Process.Pid) {
		t.Fatal("setsid-detached process reported a controlling terminal")
	}

	if !parentHasTTY(os.Getpid()) {
		t.Log("test process itself has no tty (CI/script run) — positive case exercised via the stub in TestAutopanePass")
		return
	}
	attached := exec.Command("/bin/sleep", "30")
	if err := attached.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		attached.Process.Kill() //nolint:errcheck // test fixture cleanup only
		attached.Wait()         //nolint:errcheck
	}()
	if !parentHasTTY(attached.Process.Pid) {
		t.Fatal("tty-inheriting child reported no controlling terminal")
	}
}

// TestParentIsNestedClaude exercises the real probe (the guard fixtures stub
// it). Our own process is spawned by `go test`; what matters is that the probe
// reads the PARENT's environment rather than ours, so a process whose env
// lacks the marker reads as top-level even when ours has it.
func TestParentIsNestedClaude(t *testing.T) {
	// pid 1 (launchd) never carries the marker.
	if parentIsNestedClaude(1) {
		t.Error("pid 1 must read as top-level")
	}
	// A pid that cannot be read must default to "not nested": a false skip
	// would disable auto-open entirely.
	if parentIsNestedClaude(999999999) {
		t.Error("unreadable pid must default to not-nested")
	}
}
