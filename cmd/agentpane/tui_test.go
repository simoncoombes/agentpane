package main

// tui_test.go — the attach decision. resolveAttach is deliberately pure so
// the guarantee that matters can be tested without a registry directory: a
// PINNED session that is not in the registry yet must WAIT, never fall back
// to the §3.8a cwd guess. Autopane opens the pane during SessionStart, so
// that window is the common case, and five sessions in ~/Dev make the guess
// a coin flip that then looks correct forever.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/source/registry"
)

// threeInOneDir is the owner's actual shape: several live sessions sharing a
// working directory, newest first (registry.Sessions' own order).
func threeInOneDir(t0 time.Time) []registry.Session {
	return []registry.Session{
		{PID: 300, SessionID: "cccc-3", CWD: "/Users/x/Dev", UpdatedAt: t0},
		{PID: 200, SessionID: "bbbb-2", CWD: "/Users/x/Dev", UpdatedAt: t0.Add(-time.Minute)},
		{PID: 100, SessionID: "aaaa-1", CWD: "/Users/x/Dev", UpdatedAt: t0.Add(-2 * time.Minute)},
	}
}

func TestResolveAttach(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	three := threeInOneDir(t0)

	cases := []struct {
		name    string
		rows    []registry.Session
		cwd     string
		pinned  string
		wantID  string // "" = nothing attached
		found   bool
		pin     bool
		waiting bool
		note    bool
	}{
		{
			name: "pinned to the oldest of three in one directory",
			rows: three, cwd: "/Users/x/Dev", pinned: "aaaa-1",
			wantID: "aaaa-1", found: true, pin: true,
		},
		{
			name: "pinned id absent waits, never guesses",
			rows: three, cwd: "/Users/x/Dev", pinned: "dddd-4",
			wantID: "", found: false, pin: true, waiting: true,
		},
		{
			name: "pinned id absent with an empty registry waits too",
			rows: nil, cwd: "/Users/x/Dev", pinned: "dddd-4",
			wantID: "", found: false, pin: true, waiting: true,
		},
		{
			name: "pinned to a session in another directory still wins",
			rows: append([]registry.Session{{PID: 9, SessionID: "eeee-5", CWD: "/Users/x/web", UpdatedAt: t0}}, three...),
			cwd:  "/Users/x/Dev", pinned: "eeee-5",
			wantID: "eeee-5", found: true, pin: true,
		},
		{
			name: "unpinned takes the newest matching the cwd",
			rows: three, cwd: "/Users/x/Dev",
			wantID: "cccc-3", found: true,
		},
		{
			name: "unpinned tolerates an unclean cwd",
			rows: three, cwd: "/Users/x/Dev/",
			wantID: "cccc-3", found: true,
		},
		{
			name: "unpinned falls back to the newest anywhere, noted",
			rows: three, cwd: "/Users/x/elsewhere",
			wantID: "cccc-3", found: true, note: true,
		},
		{
			name: "unpinned with nothing live attaches to nothing",
			rows: nil, cwd: "/Users/x/Dev",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveAttach(c.rows, c.cwd, c.pinned)
			if got.Session.SessionID != c.wantID {
				t.Errorf("session = %q, want %q", got.Session.SessionID, c.wantID)
			}
			if got.Found != c.found {
				t.Errorf("found = %v, want %v", got.Found, c.found)
			}
			if got.Pinned != c.pin {
				t.Errorf("pinned = %v, want %v", got.Pinned, c.pin)
			}
			if got.Waiting != c.waiting {
				t.Errorf("waiting = %v, want %v", got.Waiting, c.waiting)
			}
			if (got.Note != "") != c.note {
				t.Errorf("note = %q, want present=%v", got.Note, c.note)
			}
			// The invariant behind the whole flag: a pin never resolves to
			// some other session, whatever the registry looks like.
			if c.pinned != "" && got.Found && got.Session.SessionID != c.pinned {
				t.Fatalf("pinned to %q but attached %q", c.pinned, got.Session.SessionID)
			}
		})
	}
}

// TestResolveAttachPinNeverGuesses hammers the specific bug: with several
// live sessions in the pane's own directory and the pinned row missing, no
// row may be attached.
func TestResolveAttachPinNeverGuesses(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := threeInOneDir(t0)
	for _, pinned := range []string{"not-here", "cccc", "aaaa-1x", "CCCC-3"} {
		got := resolveAttach(rows, "/Users/x/Dev", pinned)
		if got.Found || got.Session.SessionID != "" {
			t.Fatalf("pin %q attached %q — the cwd guess leaked back in", pinned, got.Session.SessionID)
		}
		if !got.Waiting || !got.Pinned {
			t.Fatalf("pin %q: waiting=%v pinned=%v, want both true", pinned, got.Waiting, got.Pinned)
		}
	}
}

// --- waitForPinned ---
//
// The pinned pane's other half: resolveAttach refuses to guess while the row
// is missing, and waitForPinned is what eventually attaches when the row shows
// up. Untested, it could attach to the wrong row, attach twice, or outlive the
// app.

// writeRegistrySession writes one live registry row. The pid must be this
// process's: liveness is kill(pid, 0), and a dead pid is skipped as stale.
func writeRegistrySession(t *testing.T, dir string, pid int, sessionID, cwd string) {
	t.Helper()
	body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"name":"n","status":"busy","waitingFor":""}`, sessionID, cwd)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestAttacher builds an attacher whose source stack touches nothing real:
// --source transcript keeps it off the per-session hook socket (a listener
// would unlink a live pane's socket), and HOME is redirected so the
// transcript directory it computes cannot exist.
func newTestAttacher(t *testing.T, ctx context.Context, regDir string) *attacher {
	t.Helper()
	setFakeHome(t, t.TempDir())
	return &attacher{
		appCtx: ctx,
		out:    make(chan event.Event, 256),
		regDir: regDir,
		fl:     cliFlags{source: "transcript"},
		dbg:    &debugLog{},
	}
}

// attachGen reads the attach generation counter: one attach == one increment.
func attachGen(a *attacher) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gen
}

// TestWaitForPinnedAttachesOnceWhenTheRowAppears: while the pinned id is
// absent nothing is attached — not even the sibling sessions sharing the
// pane's directory, which is the whole bug — and when the row lands the pane
// attaches to it exactly once and the poller returns.
func TestWaitForPinnedAttachesOnceWhenTheRowAppears(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	regDir := t.TempDir()
	att := newTestAttacher(t, ctx, regDir)
	// Every row counts as live: registry files are keyed by pid and only one
	// of them can be this test process's.
	reg := registry.New(regDir, registry.WithLiveness(func(int) bool { return true }))

	// A decoy sibling in the same directory, already live.
	writeRegistrySession(t, regDir, os.Getpid(), "sibling-2", "/Users/x/Dev")

	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForPinned(ctx, reg, att, "pinned-1", 2*time.Millisecond)
	}()

	// Many poll intervals with the pinned row still missing.
	time.Sleep(100 * time.Millisecond)
	if got := att.current(); got != "" {
		t.Fatalf("attached %q while the pinned row was missing — the guess leaked back in", got)
	}
	if g := attachGen(att); g != 0 {
		t.Fatalf("attach generation = %d before the pinned row existed, want 0", g)
	}

	writeRegistrySession(t, regDir, os.Getpid()+1, "pinned-1", "/Users/x/Dev")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForPinned never attached after the pinned row appeared")
	}
	if got := att.current(); got != "pinned-1" {
		t.Fatalf("attached %q, want pinned-1", got)
	}
	if g := attachGen(att); g != 1 {
		t.Fatalf("attach generation = %d, want exactly 1 (one attach, then the poller stops)", g)
	}

	// The poller is gone, so a later registry change cannot re-attach.
	writeRegistrySession(t, regDir, os.Getpid()+2, "sibling-3", "/Users/x/Dev")
	time.Sleep(20 * time.Millisecond)
	if g := attachGen(att); g != 1 {
		t.Fatalf("attach generation = %d after the wait returned, want it to stay 1", g)
	}
}

// TestWaitForPinnedStopsOnContextCancel: the pane quits while still waiting.
// The poller must return promptly rather than outlive the app.
func TestWaitForPinnedStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	regDir := t.TempDir()
	att := newTestAttacher(t, ctx, regDir)
	// Every row counts as live: registry files are keyed by pid and only one
	// of them can be this test process's.
	reg := registry.New(regDir, registry.WithLiveness(func(int) bool { return true }))
	writeRegistrySession(t, regDir, os.Getpid(), "sibling-2", "/Users/x/Dev")

	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForPinned(ctx, reg, att, "never-appears", time.Millisecond)
	}()

	time.Sleep(10 * time.Millisecond) // let it get into the poll loop
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForPinned did not return after ctx cancel — the poll goroutine leaks")
	}
	if g := attachGen(att); g != 0 {
		t.Fatalf("attach generation = %d, want 0: a cancelled wait attaches nothing", g)
	}
}

// TestNewestForCWD covers the tie-breaks the §3.8a rule relies on, matching
// registry.AttachTarget: newest UpdatedAt, then highest pid.
func TestNewestForCWD(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []registry.Session{
		{PID: 10, SessionID: "a", CWD: "/w", UpdatedAt: t0},
		{PID: 40, SessionID: "b", CWD: "/w", UpdatedAt: t0}, // same instant, higher pid
		{PID: 50, SessionID: "c", CWD: "/w", UpdatedAt: t0.Add(-time.Hour)},
		{PID: 60, SessionID: "d", CWD: "/other", UpdatedAt: t0.Add(time.Hour)},
	}
	got, ok := newestForCWD(rows, "/w")
	if !ok || got.SessionID != "b" {
		t.Fatalf("newestForCWD = %q/%v, want b/true", got.SessionID, ok)
	}
	if _, ok := newestForCWD(rows, "/nothing-here"); ok {
		t.Fatal("a directory with no session must not match")
	}
}

// --- the registry is the session-agnostic source ---
//
// hooksrc and transcript are bound to one session by construction. The
// registry is not: it reports EVERY live session, including a sibling's
// "waiting on permission", and it rides in every stack. ui.Model gates each
// event on state.Machine.Accepts, which is the guarantee; filtering the
// registry here is the second lock, so the sibling's row never enters the
// process at all.

// writeWaitingRegistrySession writes a live row already blocked on a
// permission prompt — the exact state that makes the registry emit.
func writeWaitingRegistrySession(t *testing.T, dir string, pid int, sessionID, cwd string) {
	t.Helper()
	body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"name":"n","status":"waiting","waitingFor":"permission prompt"}`,
		sessionID, cwd)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAttacherScope: which session the source stack belongs to. The pin wins
// even before anything is attached, because that window — pane open, registry
// row not written yet — is precisely when a sibling's rows would arrive.
func TestAttacherScope(t *testing.T) {
	cases := []struct {
		name string
		pin  string
		sess string
		want string
	}{
		{"pinned and attached", "pinned-1", "pinned-1", "pinned-1"},
		{"pinned while still waiting", "pinned-1", "", "pinned-1"},
		{"unpinned guess", "", "guessed-2", "guessed-2"},
		{"unpinned idle watch", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &attacher{fl: cliFlags{session: c.pin}}
			if got := a.scope(registry.Session{SessionID: c.sess}); got != c.want {
				t.Fatalf("scope = %q, want %q", got, c.want)
			}
		})
	}
}

// TestPinnedAttachFiltersTheRegistrySource is the end-to-end: a sibling
// session blocked on a permission prompt must produce nothing on a pinned
// pane's event channel.
func TestPinnedAttachFiltersTheRegistrySource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	regDir := t.TempDir()
	att := newTestAttacher(t, ctx, regDir)
	att.fl.session = "pinned-1"

	// Liveness is kill(pid,0): this process, and pid 1, which answers EPERM.
	writeWaitingRegistrySession(t, regDir, os.Getpid(), "pinned-1", "/Users/x/Dev")
	writeWaitingRegistrySession(t, regDir, 1, "sibling-2", "/Users/x/Dev")

	if err := att.attach(registry.Session{SessionID: "pinned-1", CWD: "/Users/x/Dev"}); err != nil {
		t.Fatal(err)
	}

	var mine, foreign int
	deadline := time.After(3 * time.Second)
	for mine == 0 {
		select {
		case ev := <-att.out:
			switch ev.SessionID {
			case "pinned-1":
				mine++
			case "":
				// transport lifecycle: global, correctly unfiltered
			default:
				foreign++
				t.Errorf("session %q reached a pane pinned to pinned-1: %+v", ev.SessionID, ev)
			}
		case <-deadline:
			t.Fatal("the pinned session's own registry row never arrived")
		}
	}
	// Drain whatever else the first scans produced.
	settle := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-att.out:
			if ev.SessionID != "" && ev.SessionID != "pinned-1" {
				foreign++
				t.Errorf("session %q reached a pinned pane: %+v", ev.SessionID, ev)
			}
		case <-settle:
			break drain
		}
	}
	if foreign > 0 {
		t.Fatalf("%d foreign events reached the pinned pane's channel", foreign)
	}
}
