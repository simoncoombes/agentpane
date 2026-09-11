package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoAttachTargetInLiveAttachPath pins runTUI's WIRING, in the same spirit
// as TestNoProcessSignals.
//
// registry.AttachTarget answers "newest live session for this cwd". That is
// exactly the guess the --session pin exists to remove: the owner runs five
// sessions in one directory, autopane opens each pane DURING SessionStart, and
// the pinned row routinely reaches the registry a moment after the pane does.
// A pane that guesses in that window attaches to a sibling and then looks
// correct forever.
//
// resolveAttach (a pure function over a registry snapshot, cwd and the pinned
// id) is the only decision-maker in the attach path, and the tests around it
// prove a pin never resolves to another session. None of that constrains a
// future edit that simply calls reg.AttachTarget(cwd) in runTUI again — the
// suite would stay green while the original bug came back. This scan is that
// constraint: the sole permitted call site is doctor.go, which reports what
// the §3.8a rule WOULD pick as a diagnostic and attaches nothing.
func TestNoAttachTargetInLiveAttachPath(t *testing.T) {
	root := filepath.Join("..", "..")
	const needle = ".AttachTarget(" // a call, not the declaration or a comment
	allowed := map[string]bool{
		filepath.Join("cmd", "agentpane", "doctor.go"): true, // diagnostic only
	}

	hits := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "design" || name == "docs" || name == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if !strings.Contains(string(data), needle) {
			return nil
		}
		if allowed[rel] {
			hits++
			return nil
		}
		t.Errorf("%s calls registry.AttachTarget - the live attach path must go through "+
			"resolveAttach, which never guesses for a pinned pane (a pinned pane must "+
			"never show another session's data)", rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// If doctor stops calling it, this guard has quietly stopped guarding
	// anything and should be re-aimed rather than left to pass vacuously.
	if hits == 0 {
		t.Error("no permitted AttachTarget call site found - update this guard to match the new wiring")
	}
}

// TestRunTUIResolvesAttachThroughTheSessionPin is the positive half: the live
// attach path still hands the pinned id to resolveAttach. Without it the scan
// above passes on a runTUI that resolved nothing at all.
func TestRunTUIResolvesAttachThroughTheSessionPin(t *testing.T) {
	data, err := os.ReadFile("tui.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "resolveAttach(reg.Sessions(), cwd, fl.session)") {
		t.Error("runTUI no longer resolves its attach from (registry snapshot, cwd, --session) - " +
			"the pin is what keeps a pane on its own session")
	}
}
