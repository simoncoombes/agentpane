package hooksrc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

var testClock = func() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
}

// shortSocket returns a socket path short enough for the darwin sun_path
// limit (t.TempDir under /var/folders is too long).
func shortSocket(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "apane-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func newTestSource(t *testing.T, socket string) *Source {
	t.Helper()
	s := New(socket)
	s.clock = testClock
	s.minBackoff = 5 * time.Millisecond
	s.maxBackoff = 20 * time.Millisecond
	return s
}

func recvEvent(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	panic("unreachable")
}

func recvKind(t *testing.T, ch <-chan event.Event, want event.Kind) event.Event {
	t.Helper()
	ev := recvEvent(t, ch)
	if ev.Kind != want {
		t.Fatalf("Kind = %v, want %v", ev.Kind, want)
	}
	return ev
}

func dial(t *testing.T, socket string) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			return conn
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("dial: %v", err)
	panic("unreachable")
}

func TestSocketPath(t *testing.T) {
	got := SocketPath("292ef2e2-abcd")
	want := filepath.Join(os.TempDir(), "agentpane-292ef2e2-abcd.sock")
	if got != want {
		t.Errorf("SocketPath = %q, want %q", got, want)
	}

	// A hostile session id must not escape TempDir.
	hostile := SocketPath("../../etc/x")
	if filepath.Dir(hostile) != filepath.Clean(os.TempDir()) {
		t.Errorf("hostile session id escaped TempDir: %q", hostile)
	}
}

func TestSourceEmitsMappedEventsWithMonotonicSeq(t *testing.T) {
	socket := shortSocket(t)
	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	// Two concurrent forwarder-style connections, interleaved writes,
	// with junk and a torn line mixed in.
	c1 := dial(t, socket)
	c2 := dial(t, socket)

	writeLine := func(c net.Conn, s string) {
		t.Helper()
		if _, err := c.Write([]byte(s + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	writeLine(c1, capUserPromptSubmit)
	writeLine(c2, capSubagentStart)
	writeLine(c1, `total junk not json`)
	writeLine(c2, capSubagentStop)
	// Torn line: connection closes mid-payload, no trailing newline.
	c1.Write([]byte(`{"session_id":"292ef2e2-...","hook_event_name":"PreToo`))
	c1.Close()
	c2.Close()

	var got []event.Event
	for range 3 {
		got = append(got, recvEvent(t, ch))
	}

	kinds := map[event.Kind]int{}
	lastSeq := uint64(0) // SourceConnected consumed Seq 1
	for _, ev := range got {
		kinds[ev.Kind]++
		if ev.Seq <= lastSeq && lastSeq != 0 {
			t.Errorf("Seq not strictly increasing: %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		if ev.At.IsZero() {
			t.Error("At not stamped")
		}
		if !ev.At.Equal(testClock()) {
			t.Errorf("At = %v, want injected clock time", ev.At)
		}
	}
	if kinds[event.UserPromptSubmitted] != 1 || kinds[event.AgentStarted] != 1 || kinds[event.AgentReturned] != 1 {
		t.Errorf("unexpected kinds: %v", kinds)
	}

	// The junk line and the torn line were counted, not fatal.
	deadline := time.Now().Add(2 * time.Second)
	for src.Skipped() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := src.Skipped(); n < 2 {
		t.Errorf("Skipped = %d, want >= 2", n)
	}

	// The source is still alive after junk: a new connection still works.
	c3 := dial(t, socket)
	writeLine(c3, capSessionEnd)
	c3.Close()
	ev := recvKind(t, ch, event.SessionEnd)
	if ev.Seq <= lastSeq {
		t.Errorf("Seq regressed after junk: %d after %d", ev.Seq, lastSeq)
	}
}

func TestSourceDisconnectedThenRecovers(t *testing.T) {
	// A socket path inside a directory that does not exist yet: listen
	// fails, SourceDisconnected is emitted, and once the directory
	// appears the retry loop recovers and emits SourceConnected.
	dir := filepath.Join(os.TempDir(), "apane-dc-"+filepath.Base(t.Name()))
	os.RemoveAll(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")

	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ev := recvKind(t, ch, event.SourceDisconnected)
	if ev.Detail == "" {
		t.Error("SourceDisconnected should carry a reason in Detail")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	// Transport works after recovery.
	c := dial(t, socket)
	c.Write([]byte(capSessionStart + "\n"))
	c.Close()
	recvKind(t, ch, event.SessionStart)
}

func TestSourceChannelClosesOnContextDone(t *testing.T) {
	socket := shortSocket(t)
	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	cancel()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed, as required
			}
		case <-deadline:
			t.Fatal("channel not closed after context cancel")
		}
	}
}

func TestSourceRemovesStaleSocketFile(t *testing.T) {
	socket := shortSocket(t)
	// Simulate a crashed previous instance leaving the socket file behind.
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // closing removes it; recreate the stale file manually
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	c := dial(t, socket)
	c.Write([]byte(capSessionStart + "\n"))
	c.Close()
	recvKind(t, ch, event.SessionStart)
}

// TestCancelledSourceLeavesSuccessorSocket: on a session re-attach the old
// listener's teardown must not unlink the replacement's freshly bound socket
// — that would leave the new listener accepting on an unlinked inode while
// every forwarder dials ENOENT, silently killing hook coverage.
func TestCancelledSourceLeavesSuccessorSocket(t *testing.T) {
	socket := shortSocket(t)

	old := newTestSource(t, socket)
	oldCtx, cancelOld := context.WithCancel(context.Background())
	oldCh, err := old.Events(oldCtx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, oldCh, event.SourceConnected)

	// The successor binds the same path (attach cancels old, starts new).
	succ := newTestSource(t, socket)
	succCtx, cancelSucc := context.WithCancel(context.Background())
	defer cancelSucc()
	succCh, err := succ.Events(succCtx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, succCh, event.SourceConnected)

	// Old stack torn down AFTER the successor owns the path; wait for its
	// loop to finish (channel close) so any cleanup has already run.
	cancelOld()
	for range oldCh { //nolint:revive // drain until close
	}

	// The successor's socket file must still exist and still deliver.
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("successor's socket unlinked by the old listener: %v", err)
	}
	c := dial(t, socket)
	c.Write([]byte(capSessionStart + "\n"))
	c.Close()
	recvKind(t, succCh, event.SessionStart)
}

func TestSourceName(t *testing.T) {
	if got := New("x").Name(); got != "hooks" {
		t.Errorf("Name = %q", got)
	}
}

func TestSourceOversizedLineCountedNotFatal(t *testing.T) {
	socket := shortSocket(t)
	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	c := dial(t, socket)
	huge := `{"session_id":"s","hook_event_name":"PreToolUse","pad":"` + strings.Repeat("x", scanMaxBuf+1) + `"}`
	c.Write([]byte(huge + "\n"))
	c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for src.Skipped() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if src.Skipped() < 1 {
		t.Fatal("oversized line not counted as skipped")
	}

	// Still serving.
	c2 := dial(t, socket)
	c2.Write([]byte(capSessionStart + "\n"))
	c2.Close()
	recvKind(t, ch, event.SessionStart)
}
