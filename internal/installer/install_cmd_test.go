package installer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	home, _ := os.UserHomeDir()
	defaultSettings := filepath.Join(home, ".claude", "settings.json")

	t.Run("defaults", func(t *testing.T) {
		f, err := parseFlags(nil)
		if err != nil {
			t.Fatal(err)
		}
		if f.settings != defaultSettings {
			t.Errorf("settings = %q, want %q", f.settings, defaultSettings)
		}
		if f.dryRun || f.assumeYes || f.uninstall || f.autopane || f.printSnippet {
			t.Errorf("a flag defaulted to on: %+v", f)
		}
	})

	t.Run("both spellings of a valued flag", func(t *testing.T) {
		for _, args := range [][]string{
			{"--binary", "/x/agentpane", "--settings", "/y/s.json"},
			{"--binary=/x/agentpane", "--settings=/y/s.json"},
		} {
			f, err := parseFlags(args)
			if err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			if f.binary != "/x/agentpane" || f.settings != "/y/s.json" {
				t.Errorf("%v parsed as %+v", args, f)
			}
		}
	})

	t.Run("a valued flag with nothing after it", func(t *testing.T) {
		for _, args := range [][]string{{"--binary"}, {"--settings"}} {
			if _, err := parseFlags(args); err == nil {
				t.Errorf("%v: accepted a flag with no value", args)
			} else if code(err) != 2 {
				t.Errorf("%v: exit %d, want 2 (environment error)", args, code(err))
			}
		}
	})

	t.Run("an unknown flag is an error, not a silent skip", func(t *testing.T) {
		_, err := parseFlags([]string{"--nope"})
		if err == nil || code(err) != 2 {
			t.Errorf("--nope gave %v (exit %d), want an exit-2 error", err, code(err))
		}
	})

	t.Run("the value after a flag is not reparsed as one", func(t *testing.T) {
		f, err := parseFlags([]string{"--settings", "--yes"})
		if err != nil {
			t.Fatal(err)
		}
		if f.settings != "--yes" {
			t.Errorf("settings = %q, want the literal value", f.settings)
		}
		if f.assumeYes {
			t.Error("--yes was consumed as a value AND as a flag")
		}
	})
}

// code is the exit status cmdInstall would return for an error.
func code(err error) int {
	var c interface{ ExitCode() int }
	if errors.As(err, &c) {
		return c.ExitCode()
	}
	return -1
}

// The write is the part that can lose somebody's settings, so it is tested
// on every platform rather than only where it ships.
func TestWriteSettings(t *testing.T) {
	t.Run("creates the file and its directory", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "settings.json")
		if err := writeSettings(path, "{\n  \"a\": 1\n}\n"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "{\n  \"a\": 1\n}\n" {
			t.Errorf("content = %q", string(got))
		}
	})

	t.Run("backs the old file up before replacing it", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeSettings(path, "new\n"); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != "new\n" {
			t.Errorf("not replaced: %q", string(got))
		}
		entries, _ := os.ReadDir(dir)
		found := ""
		for _, e := range entries {
			if strings.Contains(e.Name(), ".agentpane-backup-") {
				found = e.Name()
			}
		}
		if found == "" {
			t.Fatalf("no backup written; files: %v", names(entries))
		}
		if got, _ := os.ReadFile(filepath.Join(dir, found)); string(got) != "old\n" {
			t.Errorf("the backup does not hold the old content: %q", string(got))
		}
	})

	// A settings.json symlinked into a dotfiles repo must have its TARGET
	// rewritten; replacing the link would quietly detach it from whatever
	// manages it.
	t.Run("writes through a symlink", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real.json")
		link := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(real, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		if err := writeSettings(link, "new\n"); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Error("the symlink was replaced by a regular file")
		}
		if got, _ := os.ReadFile(real); string(got) != "new\n" {
			t.Errorf("the target was not rewritten: %q", string(got))
		}
	})

	t.Run("leaves no temp file behind", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		if err := writeSettings(path, "x\n"); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.Contains(e.Name(), "agentpane-tmp") {
				t.Errorf("temp file left behind: %s", e.Name())
			}
		}
	})
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The snippet is what somebody pastes in by hand, so it has to be the same
// JSON the installer would have written.
func TestPrintSnippetIsValidAndComplete(t *testing.T) {
	out := captureStdout(t, func() {
		if err := printSnippet("/x/bin/agentpane"); err != nil {
			t.Fatal(err)
		}
	})
	v, err := parseJSON([]byte(out))
	if err != nil {
		t.Fatalf("the snippet is not valid JSON: %v\n%s", err, out)
	}
	hooks := v.get("hooks")
	if !hooks.isObject() {
		t.Fatalf("no hooks object:\n%s", out)
	}
	for _, ev := range hookEvents {
		if !hooks.get(ev).isArray() {
			t.Errorf("the snippet is missing %s:\n%s", ev, out)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close() //nolint:errcheck // test plumbing
	os.Stdout = prev
	return <-done
}
