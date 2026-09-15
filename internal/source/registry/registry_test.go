package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

var testNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func writeSession(t *testing.T, dir string, pid int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sessionBody(sessionID, cwd, status, waitingFor, updatedAt string) string {
	return fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"name":"n","status":%q,"waitingFor":%q,"statusUpdatedAt":%q}`,
		sessionID, cwd, status, waitingFor, updatedAt)
}

func allAlive(int) bool { return true }

// Default liveness: the test's own pid is live; an out-of-range pid is not.
// Stale registry files persist after exit (DATA-SOURCES §8 Q6) and must be
// skipped.
func TestSessionsSkipsStalePIDFiles(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, os.Getpid(), sessionBody("s-live", "/a", "busy", "", "2026-08-01T10:00:00Z"))
	writeSession(t, dir, 99999999, sessionBody("s-stale", "/a", "busy", "", "2026-08-01T11:00:00Z"))
	writeSession(t, dir, os.Getpid()+1, `{broken json`) // malformed: skipped, never fatal

	s := New(dir)
	got := s.Sessions()
	if len(got) != 1 {
		t.Fatalf("Sessions() len = %d, want 1 (stale + malformed skipped)", len(got))
	}
	row := got[0]
	if row.PID != os.Getpid() || row.SessionID != "s-live" || row.CWD != "/a" || row.Status != "busy" {
		t.Errorf("row = %+v", row)
	}
	wantAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if !row.UpdatedAt.Equal(wantAt) {
		t.Errorf("UpdatedAt = %v, want %v", row.UpdatedAt, wantAt)
	}
}

func TestAttachTargetNewestLiveWins(t *testing.T) {
	dir := t.TempDir()
	older := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 1, 10, 5, 0, 0, time.UTC)

	writeSession(t, dir, 101, sessionBody("s-old", "/a", "busy", "", older.Format(time.RFC3339)))
	// Numeric epoch-ms statusUpdatedAt must parse too (wire shape unverified).
	writeSession(t, dir, 102, fmt.Sprintf(
		`{"sessionId":"s-new","cwd":"/a","status":"busy","statusUpdatedAt":%d}`, newer.UnixMilli()))
	writeSession(t, dir, 103, sessionBody("s-other", "/b", "busy", "", newer.Format(time.RFC3339)))
	// A newer session in the right cwd that is dead must lose.
	writeSession(t, dir, 900, sessionBody("s-dead", "/a", "busy", "",
		time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)))

	s := New(dir, WithLiveness(func(pid int) bool { return pid != 900 }))

	got, ok := s.AttachTarget("/a")
	if !ok || got.SessionID != "s-new" {
		t.Fatalf("AttachTarget(/a) = %+v, %t; want s-new", got, ok)
	}
	if !got.UpdatedAt.Equal(newer) {
		t.Errorf("UpdatedAt = %v, want %v (epoch ms parse)", got.UpdatedAt, newer)
	}
	if got, ok := s.AttachTarget("/a/"); !ok || got.SessionID != "s-new" {
		t.Errorf("AttachTarget with trailing slash = %+v, %t; want s-new", got, ok)
	}
	if _, ok := s.AttachTarget("/nowhere"); ok {
		t.Error("AttachTarget(/nowhere) matched, want none")
	}
}

func TestScanStatusTransitions(t *testing.T) {
	dir := t.TempDir()
	s := New(dir,
		WithLiveness(allAlive),
		WithClock(func() time.Time { return testNow }),
	)

	steps := []struct {
		name      string
		body      string
		wantKinds []event.Kind
	}{
		{"first observation busy emits nothing", sessionBody("s1", "/a", "busy", "", ""), nil},
		{"transition to waiting on permission", sessionBody("s1", "/a", "waiting", "permission prompt", ""),
			[]event.Kind{event.NotificationRaised}},
		{"no change re-emits nothing", sessionBody("s1", "/a", "waiting", "permission prompt", ""), nil},
		{"waiting on something else emits nothing", sessionBody("s1", "/a", "waiting", "elicitation", ""), nil},
		{"transition to idle", sessionBody("s1", "/a", "idle", "", ""),
			[]event.Kind{event.SessionIdle}},
	}
	for _, step := range steps {
		writeSession(t, dir, 201, step.body)
		got := s.scan()
		var kinds []event.Kind
		for _, e := range got {
			kinds = append(kinds, e.Kind)
		}
		if len(kinds) != len(step.wantKinds) {
			t.Fatalf("%s: kinds = %v, want %v", step.name, kinds, step.wantKinds)
		}
		for i := range kinds {
			if kinds[i] != step.wantKinds[i] {
				t.Fatalf("%s: kinds = %v, want %v", step.name, kinds, step.wantKinds)
			}
		}
		for _, e := range got {
			if e.SessionID != "s1" || e.AgentID != event.MainAgentID {
				t.Errorf("%s: event ids = %+v", step.name, e)
			}
			if !e.At.Equal(testNow) {
				t.Errorf("%s: At = %v, want ingest clock (statusUpdatedAt is advisory)", step.name, e.At)
			}
			if e.Kind == event.NotificationRaised && e.Detail != waitingDetail {
				t.Errorf("%s: Detail = %q, want %q", step.name, e.Detail, waitingDetail)
			}
		}
	}
}

// A session already waiting when first seen is attention-worthy: the first
// observation must emit, not wait for a later transition.
func TestScanFirstObservationWaitingEmits(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, WithLiveness(allAlive), WithClock(func() time.Time { return testNow }))
	writeSession(t, dir, 202, sessionBody("s2", "/b", "waiting", "permission prompt", ""))

	got := s.scan()
	if len(got) != 1 || got[0].Kind != event.NotificationRaised || got[0].Detail != waitingDetail {
		t.Fatalf("scan = %+v, want one NotificationRaised confirmer", got)
	}
}

func TestScanForgetsRemovedSessions(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, WithLiveness(allAlive), WithClock(func() time.Time { return testNow }))
	writeSession(t, dir, 203, sessionBody("s3", "/c", "idle", "", ""))
	if got := s.scan(); len(got) != 1 || got[0].Kind != event.SessionIdle {
		t.Fatalf("first scan = %+v", got)
	}
	if err := os.Remove(filepath.Join(dir, "203.json")); err != nil {
		t.Fatal(err)
	}
	if got := s.scan(); len(got) != 0 {
		t.Fatalf("scan after removal = %+v, want none", got)
	}
	// Re-appearing in the same state is a fresh observation and emits again.
	writeSession(t, dir, 203, sessionBody("s3", "/c", "idle", "", ""))
	if got := s.scan(); len(got) != 1 || got[0].Kind != event.SessionIdle {
		t.Fatalf("scan after reappearance = %+v, want SessionIdle", got)
	}
}

func TestEventsChannelDelivers(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, 301, sessionBody("s4", "/d", "waiting", "permission prompt", ""))
	s := New(dir,
		WithLiveness(allAlive),
		WithClock(func() time.Time { return testNow }),
		WithPollInterval(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != event.NotificationRaised || ev.Detail != waitingDetail {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NotificationRaised")
	}
}

// The missing-directory case is the normal nothing-running state, never an
// error (C9).
func TestMissingDirIsNotAnError(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if got := s.Sessions(); len(got) != 0 {
		t.Fatalf("Sessions() = %+v, want empty", got)
	}
	if got := s.scan(); len(got) != 0 {
		t.Fatalf("scan() = %+v, want no transport events", got)
	}
}

// TestSessionFilterEmitsOnlyThePinnedSession: the registry is the one source
// that reports EVERY live session, so a sibling hitting a permission prompt
// used to hand a pinned pane a NotificationRaised for a session it must never
// show. With a filter the row never leaves this source.
func TestSessionFilterEmitsOnlyThePinnedSession(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, WithLiveness(allAlive), WithClock(func() time.Time { return testNow }),
		WithSessionFilter("s-mine"))

	writeSession(t, dir, 401, sessionBody("s-mine", "/a", "waiting", "permission prompt", ""))
	writeSession(t, dir, 402, sessionBody("s-other", "/a", "waiting", "permission prompt", ""))
	writeSession(t, dir, 403, sessionBody("", "/a", "idle", "", "")) // unattributable row

	got := s.scan()
	if len(got) != 1 {
		t.Fatalf("scan emitted %d events, want only s-mine's: %+v", len(got), got)
	}
	if got[0].SessionID != "s-mine" || got[0].Kind != event.NotificationRaised {
		t.Fatalf("emitted %+v, want s-mine NotificationRaised", got[0])
	}

	// Sessions() is unfiltered: it is the discovery/idle-screen read, and the
	// pinned wait needs to see a session that is not attached yet.
	if rows := s.Sessions(); len(rows) != 3 {
		t.Fatalf("Sessions() = %d rows, want all 3 — the filter is on the event stream only", len(rows))
	}

	// The sibling transitioning again still emits nothing, and the filter has
	// not corrupted the diff bookkeeping for the row it does watch.
	writeSession(t, dir, 402, sessionBody("s-other", "/a", "idle", "", ""))
	writeSession(t, dir, 401, sessionBody("s-mine", "/a", "idle", "", ""))
	got = s.scan()
	if len(got) != 1 || got[0].SessionID != "s-mine" || got[0].Kind != event.SessionIdle {
		t.Fatalf("second scan = %+v, want one s-mine SessionIdle", got)
	}
}

// TestSessionFilterKeepsTransportEvents: SourceConnected/SourceDisconnected
// carry no session id because they are about the registry itself, not about
// one session. A filtered source must still report losing the directory.
func TestSessionFilterKeepsTransportEvents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(dir, WithLiveness(allAlive), WithClock(func() time.Time { return testNow }),
		WithSessionFilter("s-mine"))
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skip("cannot make the directory unreadable here")
	}
	defer os.Chmod(dir, 0o755) //nolint:errcheck // test cleanup
	// Chmod SUCCEEDING is not the same as the directory becoming unreadable:
	// on Windows it only toggles the read-only attribute and the read below
	// would go right through, so the transport failure this test is about
	// never happens. Check the effect, not the call.
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("the directory is still readable after chmod 000; no transport failure to observe here")
	}

	got := s.scan()
	if len(got) != 1 || got[0].Kind != event.SourceDisconnected {
		t.Fatalf("scan = %+v, want SourceDisconnected through the filter", got)
	}
	if got[0].SessionID != "" {
		t.Fatalf("transport event carries a session id %q", got[0].SessionID)
	}
}
