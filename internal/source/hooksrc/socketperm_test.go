package hooksrc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The socket lives in a directory every local account can write to and it
// carries prompts, tool inputs and file paths out of the session. Default
// socket permissions would let any local user connect to it.
func TestSocketIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Mode bits are not the mechanism there. os.Chmod only toggles the
		// read-only attribute on Windows, so the socket reads as 0666 however
		// it was created; what actually restricts it is the ACL on the
		// directory, and os.TempDir is %LOCALAPPDATA%\Temp — already private
		// to the user, unlike the /tmp this test exists because of.
		t.Skip("file mode does not carry permissions on Windows; the per-user temp directory's ACL does")
	}
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
