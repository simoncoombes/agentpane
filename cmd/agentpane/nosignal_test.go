package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoProcessSignals enforces §5.3 / PART 10 bullet 5 at the source level:
// NO code path anywhere sends a signal to any process. The single allowed
// use is the registry's liveness probe kill(pid, 0), which delivers no
// signal (POSIX: sig 0 performs error checking only).
func TestNoProcessSignals(t *testing.T) {
	root := filepath.Join("..", "..")
	probe := "syscall.Kill(pid, 0)"
	allowedFile := filepath.Join("internal", "source", "registry", "registry.go")

	needles := []string{".Process.Kill(", ".Process.Signal(", "syscall.Kill("}

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
		src := string(data)
		rel, _ := filepath.Rel(root, path)
		for _, needle := range needles {
			for i := 0; ; {
				j := strings.Index(src[i:], needle)
				if j < 0 {
					break
				}
				j += i
				i = j + len(needle)
				if rel == allowedFile && needle == "syscall.Kill(" && strings.HasPrefix(src[j:], probe) {
					continue // the documented liveness probe
				}
				t.Errorf("%s: forbidden process-signal call %q (§5.3: agentpane never signals a process)", rel, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
