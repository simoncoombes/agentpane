//go:build !windows

package installer

// installer_unix_test.go — the tests for the bash path. They run the embedded
// script, so they belong where that path is compiled; the Windows installer
// has no script to run and is covered by hooks_test.go instead.

import (
	"os"
	"path/filepath"
	"testing"
)

// Run executes the embedded script rather than looking for one on disk: this
// is the whole reason the package exists, so it is asserted from a directory
// with no install.sh anywhere above it.
func TestRunNeedsNoScriptOnDisk(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd) //nolint:errcheck // test teardown

	// A binary named anything else is refused by the script, so the fixture
	// has to carry the real name.
	bin := filepath.Join(dir, "agentpane")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"--dry-run", "--settings", settings}, bin); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	// --dry-run writes nothing.
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != `{"hooks":{}}` {
		t.Errorf("a dry run changed the settings file: %s", after)
	}
}

// The running executable is what the hook lines must name — a binary in
// ~/.local/bin and one in /opt each wire themselves — but a caller who named a
// binary keeps it.
func TestBinaryFlagIsNotOverridden(t *testing.T) {
	for _, args := range [][]string{
		{"--binary", "/x/agentpane"},
		{"--binary=/x/agentpane"},
	} {
		if !hasBinaryFlag(args) {
			t.Errorf("%v: --binary not detected, the caller's choice would be overridden", args)
		}
	}
	if hasBinaryFlag([]string{"--dry-run", "--binaryish"}) {
		t.Error("--binaryish was mistaken for --binary")
	}
}
