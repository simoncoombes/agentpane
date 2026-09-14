package installer

import (
	"os"
	"strings"
	"testing"
)

// The embedded copy is the script, not a stale snapshot of it.
func TestEmbeddedScriptMatchesTheFile(t *testing.T) {
	onDisk, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if Script() != string(onDisk) {
		t.Error("the embedded installer differs from install.sh")
	}
	if !strings.HasPrefix(Script(), "#!/bin/bash") {
		t.Errorf("the embedded installer is not the script: %.40q", Script())
	}
}
