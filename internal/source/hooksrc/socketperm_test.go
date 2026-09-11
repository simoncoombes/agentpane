package hooksrc

import (
	"os"
	"path/filepath"
	"testing"
)

// The socket lives in a directory every local account can write to and it
// carries prompts, tool inputs and file paths out of the session. Default
// socket permissions would let any local user connect to it.
func TestSocketIsOwnerOnly(t *testing.T) {
	// Not t.TempDir(): its path plus a socket name overruns the 104-byte
	// sun_path limit on darwin.
	dir, err := os.MkdirTemp("", "ap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode is %04o, want 0600 — a local user can connect", perm)
	}
}
