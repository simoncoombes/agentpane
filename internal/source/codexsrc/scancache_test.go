package codexsrc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRollout drops one rollout file with a session_meta first line.
func writeRollout(t *testing.T, dir, id, session, parent, source string) string {
	t.Helper()
	day := filepath.Join(dir, "2026", "09", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(day, "rollout-2026-09-10T12-00-00-"+id+".jsonl")
	line := `{"type":"session_meta","timestamp":"2026-09-10T12:00:00.000Z","payload":` +
		`{"session_id":"` + session + `","id":"` + id + `","parent_thread_id":"` + parent +
		`","thread_source":"` + source + `","cwd":"/w"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The scanner reads a session_meta line once. It is the first line of the
// file and is written at creation, so re-reading it every 400ms is the poll's
// whole cost for no new information.
//
// The proof is destructive: after a first scan, the file's meta line is
// replaced with junk. A fresh scanner loses the thread; the warm one does not,
// which it could only manage from cache.
func TestScannerReadsMetaOnce(t *testing.T) {
	dir := t.TempDir()
	p := writeRollout(t, dir, "t1", "s1", "", "user")
	now := time.Now()

	var sc scanner
	first, err := sc.scan(dir, 0, now)
	if err != nil || len(first) != 1 || len(first[0].Threads) != 1 {
		t.Fatalf("first scan: %v, %#v", err, first)
	}

	if err := os.WriteFile(p, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	warm, err := sc.scan(dir, 0, now)
	if err != nil || len(warm) != 1 {
		t.Fatalf("warm scan re-read the meta line: %v, %#v", err, warm)
	}
	if cold, err := Scan(dir, 0, now); err != nil || len(cold) != 0 {
		t.Fatalf("cold scan should find nothing in a junk file: %v, %#v", err, cold)
	}
}

// A file caught mid-creation has no complete first line yet. That is not a
// verdict, so it is not cached: the next pass tries again.
func TestScannerRetriesAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	day := filepath.Join(dir, "2026", "09", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(day, "rollout-2026-09-10T12-00-00-t2.jsonl")
	// A partial line: no newline yet, which is what a half-written file looks
	// like to a reader.
	if err := os.WriteFile(p, []byte(`{"type":"session_`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	var sc scanner
	if got, _ := sc.scan(dir, 0, now); len(got) != 0 {
		t.Fatalf("a half-written file became a session: %#v", got)
	}
	writeRollout(t, dir, "t2", "s2", "", "user")
	if got, _ := sc.scan(dir, 0, now); len(got) != 1 {
		t.Fatalf("the finished file was never re-read: %#v", got)
	}
}

// The cache tracks the window, not the whole history: a file that ages out is
// forgotten rather than held forever by a pane that has been open for days.
func TestScannerForgetsFilesOutsideTheWindow(t *testing.T) {
	dir := t.TempDir()
	writeRollout(t, dir, "t3", "s3", "", "user")
	p := writeRollout(t, dir, "t4", "s4", "", "user")
	now := time.Now()

	var sc scanner
	if got, _ := sc.scan(dir, time.Hour, now); len(got) != 2 {
		t.Fatalf("want two sessions, got %#v", got)
	}
	if len(sc.meta) != 2 {
		t.Fatalf("cache holds %d entries, want 2", len(sc.meta))
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if got, _ := sc.scan(dir, time.Hour, now); len(got) != 1 {
		t.Fatalf("the aged-out file is still in the scan: %#v", got)
	}
	if len(sc.meta) != 1 {
		t.Fatalf("cache holds %d entries after the file aged out, want 1", len(sc.meta))
	}
}
