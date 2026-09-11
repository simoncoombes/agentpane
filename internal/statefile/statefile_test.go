package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// clock is a manually advanced time source.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func readFile(t *testing.T, dir string) State {
	t.Helper()
	return readNamed(t, dir, FileName)
}

func readNamed(t *testing.T, dir, name string) State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read state file %s: %v", name, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal state file: %v", err)
	}
	return s
}

func TestWriterImmediateFirstWrite(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	w := &Writer{Dir: dir, Now: c.Now}
	w.Update(State{Session: "2a71", Live: 3})
	got := readFile(t, dir)
	if got.Session != "2a71" || got.Live != 3 {
		t.Errorf("got %+v", got)
	}
	if !got.UpdatedAt.Equal(t0) {
		t.Errorf("UpdatedAt = %v, want stamp at write time %v", got.UpdatedAt, t0)
	}
}

func TestWriterDebounce(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	var trailing func()
	var trailingDelay time.Duration
	w := &Writer{
		Dir: dir,
		Now: c.Now,
		AfterFunc: func(d time.Duration, f func()) *time.Timer {
			trailing, trailingDelay = f, d
			return time.NewTimer(time.Hour) // never fires on its own
		},
	}

	w.Update(State{Live: 1})
	c.now = t0.Add(200 * time.Millisecond)
	w.Update(State{Live: 2})
	c.now = t0.Add(400 * time.Millisecond)
	w.Update(State{Live: 3})

	if got := readFile(t, dir); got.Live != 1 {
		t.Errorf("burst must not rewrite within the interval: file has Live=%d, want 1", got.Live)
	}
	if trailing == nil {
		t.Fatal("no trailing flush scheduled — the newest state would be lost")
	}
	if trailingDelay != 800*time.Millisecond {
		t.Errorf("trailing delay = %v, want 800ms (interval minus elapsed)", trailingDelay)
	}

	c.now = t0.Add(time.Second)
	trailing()
	got := readFile(t, dir)
	if got.Live != 3 {
		t.Errorf("trailing flush wrote Live=%d, want the latest (3)", got.Live)
	}
	if !got.UpdatedAt.Equal(c.now) {
		t.Errorf("UpdatedAt = %v, want flush time %v", got.UpdatedAt, c.now)
	}

	// After a full interval, the next update writes immediately again.
	c.now = t0.Add(3 * time.Second)
	w.Update(State{Live: 4})
	if got := readFile(t, dir); got.Live != 4 {
		t.Errorf("post-interval update: file has Live=%d, want 4", got.Live)
	}
}

func TestWriterFlush(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	w := &Writer{
		Dir: dir,
		Now: c.Now,
		AfterFunc: func(d time.Duration, f func()) *time.Timer {
			return time.NewTimer(time.Hour)
		},
	}
	w.Update(State{Live: 1})
	c.now = t0.Add(100 * time.Millisecond)
	w.Update(State{Live: 2, Settled: 5})
	w.Flush()
	if got := readFile(t, dir); got.Live != 2 || got.Settled != 5 {
		t.Errorf("after Flush: %+v, want pending state on disk", got)
	}
	w.Flush() // idempotent with nothing pending
}

func TestWriterAtomicNoTempLeftovers(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	w := &Writer{Dir: dir, Now: c.Now}
	for i := 0; i < 3; i++ {
		c.now = c.now.Add(2 * time.Second)
		w.Update(State{Live: i})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Errorf("leftover file %q — write is not atomic tmp+rename", e.Name())
		}
	}
}

func TestRead(t *testing.T) {
	now := t0.Add(time.Minute)
	write := func(t *testing.T, s State) string {
		t.Helper()
		dir := t.TempDir()
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, FileName), data, 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("fresh file reads ok", func(t *testing.T) {
		dir := write(t, State{Session: "2a71", UpdatedAt: now.Add(-14 * time.Second)})
		s, ok := Read(dir, now)
		if !ok || s.Session != "2a71" {
			t.Errorf("got %+v ok=%v, want ok", s, ok)
		}
	})
	t.Run("stale file is not-running", func(t *testing.T) {
		dir := write(t, State{UpdatedAt: now.Add(-16 * time.Second)})
		if _, ok := Read(dir, now); ok {
			t.Error("UpdatedAt older than 15s must read as not-running")
		}
	})
	t.Run("missing file is not-running", func(t *testing.T) {
		if _, ok := Read(t.TempDir(), now); ok {
			t.Error("missing file must read as not-running")
		}
	})
	t.Run("corrupt file is not-running, no panic", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := Read(dir, now); ok {
			t.Error("corrupt file must read as not-running")
		}
	})
	t.Run("zero UpdatedAt is not-running", func(t *testing.T) {
		dir := write(t, State{Session: "x"})
		if _, ok := Read(dir, now); ok {
			t.Error("zero UpdatedAt must read as not-running")
		}
	})
}

// --- per-session state files ---
//
// N pinned panes are N writers. One current.json made them race, so
// `--oneline` flipped between unrelated sessions second to second.

func TestWriterKeysFileBySession(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	a := &Writer{Dir: dir, Session: "sess-aaaa", Now: c.Now}
	b := &Writer{Dir: dir, Session: "sess-bbbb", Now: c.Now}
	a.Update(State{Session: "aaaa", Live: 3})
	b.Update(State{Session: "bbbb", Live: 7})

	if got := readNamed(t, dir, "current-sess-aaaa.json"); got.Live != 3 {
		t.Errorf("session A's file has Live=%d, want 3 (B overwrote it)", got.Live)
	}
	if got := readNamed(t, dir, "current-sess-bbbb.json"); got.Live != 7 {
		t.Errorf("session B's file has Live=%d, want 7", got.Live)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Error("a keyed writer must not touch the shared current.json")
	}
}

func TestFileNameForSanitizes(t *testing.T) {
	if got := FileNameFor(""); got != FileName {
		t.Errorf("FileNameFor(\"\") = %q, want %q", got, FileName)
	}
	got := FileNameFor("../../etc/passwd")
	if strings.ContainsRune(got, filepath.Separator) {
		t.Errorf("FileNameFor produced a path %q", got)
	}
	if !isStateFile(got) {
		t.Errorf("%q is not recognised as a state file", got)
	}
}

func TestSetSessionRekeysTheFile(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	w := &Writer{Dir: dir, Session: "sess-aaaa", Now: c.Now}
	w.Update(State{Session: "aaaa", Live: 1})
	c.now = c.now.Add(2 * time.Second)
	w.SetSession("sess-bbbb")
	w.Update(State{Session: "bbbb", Live: 2})

	if got := readNamed(t, dir, "current-sess-bbbb.json"); got.Live != 2 {
		t.Errorf("after SetSession: %+v, want the write under the new session", got)
	}
	if got := readNamed(t, dir, "current-sess-aaaa.json"); got.Live != 1 {
		t.Errorf("the old session's file was rewritten: %+v", got)
	}
}

func TestReadSessionPicksExactlyOnePane(t *testing.T) {
	dir := t.TempDir()
	now := t0.Add(time.Minute)
	writeState(t, dir, "sess-aaaa", State{Session: "aaaa", Live: 1, UpdatedAt: now.Add(-10 * time.Second)})
	writeState(t, dir, "sess-bbbb", State{Session: "bbbb", Live: 9, UpdatedAt: now.Add(-time.Second)})

	got, ok := ReadSession(dir, "sess-aaaa", now)
	if !ok || got.Live != 1 {
		t.Fatalf("ReadSession(A) = %+v ok=%v; want A's own state, not the freshest pane's", got, ok)
	}
	if _, ok := ReadSession(dir, "sess-cccc", now); ok {
		t.Error("a session with no state file must read as not-running, never fall back")
	}
	// A stale pinned pane is not-running, even with a fresh sibling on disk.
	writeState(t, dir, "sess-dddd", State{Session: "dddd", UpdatedAt: now.Add(-16 * time.Second)})
	if _, ok := ReadSession(dir, "sess-dddd", now); ok {
		t.Error("stale session file must read as not-running")
	}
}

func TestReadLatestPicksNewestAndCountsLivePanes(t *testing.T) {
	dir := t.TempDir()
	now := t0.Add(time.Minute)
	writeState(t, dir, "sess-aaaa", State{Session: "aaaa", Live: 1, UpdatedAt: now.Add(-10 * time.Second)})
	writeState(t, dir, "sess-bbbb", State{Session: "bbbb", Live: 9, UpdatedAt: now.Add(-time.Second)})
	writeState(t, dir, "sess-cccc", State{Session: "cccc", Live: 4, UpdatedAt: now.Add(-time.Hour)}) // stale

	got, ok, live := ReadLatest(dir, now)
	if !ok || got.Session != "bbbb" || got.Live != 9 {
		t.Fatalf("ReadLatest = %+v ok=%v, want the freshest pane (bbbb)", got, ok)
	}
	if live != 2 {
		t.Errorf("live = %d, want 2 fresh panes (the stale one does not count)", live)
	}

	// Nothing fresh at all is the "no agentpane running" case.
	if _, ok, live := ReadLatest(dir, now.Add(time.Hour)); ok || live != 0 {
		t.Errorf("all-stale: ok=%v live=%d, want false/0", ok, live)
	}
	if _, ok, live := ReadLatest(t.TempDir(), now); ok || live != 0 {
		t.Errorf("empty dir: ok=%v live=%d, want false/0", ok, live)
	}
}

func TestReadLatestIgnoresForeignAndTempFiles(t *testing.T) {
	dir := t.TempDir()
	now := t0.Add(time.Minute)
	writeState(t, dir, "sess-aaaa", State{Session: "aaaa", Live: 1, UpdatedAt: now})
	for _, name := range []string{"ui.json", ".current-1234.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"live":99,"updated_at":"2999-01-01T00:00:00Z"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, live := ReadLatest(dir, now)
	if !ok || live != 1 || got.Live != 1 {
		t.Fatalf("ReadLatest = %+v ok=%v live=%d; a neighbouring file was mistaken for a state file", got, ok, live)
	}
}

func TestSweepRemovesAbandonedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	writeState(t, dir, "sess-dead", State{Session: "dead", UpdatedAt: t0.Add(-2 * AbandonedAfter)})
	writeState(t, dir, "sess-live", State{Session: "live", UpdatedAt: t0.Add(-time.Second)})
	if err := os.WriteFile(filepath.Join(dir, "ui.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &Writer{Dir: dir, Session: "sess-mine", Now: c.Now}
	w.Update(State{Session: "mine"})

	if _, err := os.Stat(filepath.Join(dir, "current-sess-dead.json")); err == nil {
		t.Error("an abandoned state file survived the sweep — the state dir grows without bound")
	}
	if _, err := os.Stat(filepath.Join(dir, "current-sess-live.json")); err != nil {
		t.Errorf("the sweep deleted a running pane's file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ui.json")); err != nil {
		t.Errorf("the sweep deleted a foreign file: %v", err)
	}
	readNamed(t, dir, "current-sess-mine.json") // and our own write landed
}

func writeState(t *testing.T, dir, session string, s State) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileNameFor(session)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderOneline(t *testing.T) {
	tests := []struct {
		name string
		s    State
		ok   bool
		live int
		want string
	}{
		{
			name: "spec example",
			s: State{
				Asks: 1, Live: 4, InCall: 1, Settled: 2,
				OldestAskSlug: "fix-ts2345",
				OldestAskAge:  4*time.Minute + 2*time.Second,
			},
			ok:   true,
			want: "⚑1 ●4 ◐1 ✔2 4m02s fix-ts2345",
		},
		{
			name: "zero asks omits flag, age, and slug",
			s:    State{Live: 4, InCall: 1, Settled: 2},
			ok:   true,
			want: "●4 ◐1 ✔2",
		},
		{
			name: "stalls render between live and in-call",
			s:    State{Live: 2, Stalls: 1, Settled: 3},
			ok:   true,
			want: "●2 ○1 ✔3",
		},
		{
			name: "all quiet",
			s:    State{},
			ok:   true,
			want: "●0 ✔0",
		},
		{
			name: "ask with sub-minute age",
			s:    State{Asks: 2, Live: 1, OldestAskSlug: "run-auth-tests", OldestAskAge: 52 * time.Second},
			ok:   true,
			want: "⚑2 ●1 ✔0 52s run-auth-tests",
		},
		{
			name: "hour-scale age",
			s:    State{Asks: 1, OldestAskSlug: "x", OldestAskAge: time.Hour + 4*time.Minute + 2*time.Second},
			ok:   true,
			want: "⚑1 ●0 ✔0 1h04m x",
		},
		{
			name: "negative age clamps",
			s:    State{Asks: 1, OldestAskSlug: "x", OldestAskAge: -5 * time.Second},
			ok:   true,
			want: "⚑1 ●0 ✔0 0s x",
		},
		{
			name: "not running",
			s:    State{Asks: 9},
			ok:   false,
			want: "no agentpane running",
		},
		{
			name: "several panes live: the row names its session",
			s:    State{Session: "2a71", Live: 4, Settled: 2},
			ok:   true, live: 3,
			want: "2a71 ●4 ✔2",
		},
		{
			name: "one pane live: no id, the row is already unambiguous",
			s:    State{Session: "2a71", Live: 4, Settled: 2},
			ok:   true, live: 1,
			want: "●4 ✔2",
		},
		{
			name: "several panes live but no session recorded: nothing invented",
			s:    State{Live: 1},
			ok:   true, live: 2,
			want: "●1 ✔0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := tt.live
			if live == 0 {
				live = 1
			}
			if got := RenderOneline(tt.s, tt.ok, live); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStateJSONKeys(t *testing.T) {
	data, err := json.Marshal(State{Session: "s", UpdatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"session"`, `"updated_at"`, `"live"`, `"asks"`, `"stalls"`,
		`"in_call"`, `"settled"`, `"oldest_ask_slug"`, `"oldest_ask_age"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshalled state missing §3.20a key %s: %s", key, data)
		}
	}
}
