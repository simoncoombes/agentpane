package source

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// scriptedSource is a fabricated event.Source the tests feed by hand.
type scriptedSource struct {
	name string
	ch   chan event.Event

	mu      sync.Mutex
	started bool
}

func newScripted(name string) *scriptedSource {
	return &scriptedSource{name: name, ch: make(chan event.Event)}
}

func (s *scriptedSource) Name() string { return s.name }

func (s *scriptedSource) Events(ctx context.Context) (<-chan event.Event, error) {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	return s.ch, nil
}

// emit pushes one event into the source, blocking until the mux ingests it.
func (s *scriptedSource) emit(t *testing.T, ev event.Event) {
	t.Helper()
	select {
	case s.ch <- ev:
	case <-time.After(2 * time.Second):
		t.Fatalf("mux never ingested event from %s", s.name)
	}
}

// testClock is a settable clock shared with the mux pump.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// recv reads the next forwarded event or fails.
func recv(t *testing.T, out <-chan event.Event) event.Event {
	t.Helper()
	select {
	case ev, ok := <-out:
		if !ok {
			t.Fatal("mux output closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mux output")
	}
	return event.Event{}
}

func ev(kind event.Kind, seq uint64) event.Event {
	return event.Event{Seq: seq, Kind: kind, AgentID: "a1", SessionID: "s"}
}

func toolStart(tool string, seq uint64) event.Event {
	e := ev(event.ToolStart, seq)
	e.Tool = tool
	return e
}

// startMux wires the given sources through the injectable-clock mux.
func startMux(t *testing.T, clock *testClock, sources ...event.Source) (<-chan event.Event, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, err := mux(ctx, clock.now, sources...)
	if err != nil {
		cancel()
		t.Fatalf("mux: %v", err)
	}
	t.Cleanup(cancel)
	return out, cancel
}

// TestMuxHooksSuppressTranscript covers primary mode: hooks active, the
// transcript's duplicated kinds are dropped, its exclusive kinds pass, and
// the TodoWrite exception cuts both ways.
func TestMuxHooksSuppressTranscript(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr)

	// Hooks become active.
	hooks.emit(t, ev(event.SessionStart, 1))
	if got := recv(t, out); got.Kind != event.SessionStart {
		t.Fatalf("want hooks SessionStart, got %v", got.Kind)
	}

	steps := []struct {
		src     *scriptedSource
		in      event.Event
		dropped bool
	}{
		{tr, ev(event.SessionStart, 1), true},            // duplicates hook coverage
		{tr, ev(event.UserPromptSubmitted, 2), true},     // duplicates hook coverage
		{tr, ev(event.AgentSpawned, 3), false},           // naming channel: never suppressed
		{tr, ev(event.AgentStarted, 15), false},          // naming channel: never suppressed
		{tr, toolStart("Bash", 4), true},                 // duplicates hook coverage
		{tr, ev(event.ToolEnd, 5), true},                 // duplicates hook coverage
		{tr, ev(event.FileEdited, 6), true},              // duplicates hook coverage
		{tr, ev(event.CompactStarted, 7), true},          // duplicates hook coverage
		{tr, toolStart(todoWriteTool, 8), false},         // only channel with todo text
		{tr, ev(event.TokensUpdated, 9), false},          // transcript-exclusive
		{tr, ev(event.ThinkingEnded, 10), false},         // transcript-exclusive
		{tr, ev(event.PermissionDenied, 11), false},      // transcript-exclusive
		{tr, ev(event.ErrorRaised, 12), false},           // transcript-exclusive
		{hooks, toolStart(todoWriteTool, 2), true},       // symmetric drop: transcript owns TodoWrite
		{hooks, toolStart("Bash", 3), false},             // ordinary hooks traffic passes
		{hooks, ev(event.PermissionRequested, 4), false}, // the whole point
		{tr, ev(event.AgentReturned, 13), true},          // still suppressed
		{tr, ev(event.TokensUpdated, 14), false},         // sync marker
	}
	for i, s := range steps {
		s.src.emit(t, s.in)
		if s.dropped {
			continue
		}
		got := recv(t, out)
		if got.Kind != s.in.Kind || got.Tool != s.in.Tool {
			t.Fatalf("step %d: want %v/%q, got %v/%q — a suppressed event leaked or a pass-through was dropped",
				i, s.in.Kind, s.in.Tool, got.Kind, got.Tool)
		}
	}
}

// TestMuxFallbackNoHooks covers fallback mode: without a hooks source the
// transcript passes everything, including the kinds hooks would cover.
func TestMuxFallbackNoHooks(t *testing.T) {
	tr := newScripted("transcript")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, tr)

	kinds := []event.Event{
		ev(event.SessionStart, 1),
		ev(event.UserPromptSubmitted, 2),
		ev(event.AgentSpawned, 3),
		toolStart("Bash", 4),
		toolStart(todoWriteTool, 5),
		ev(event.FileEdited, 6),
		ev(event.TokensUpdated, 7),
		ev(event.AgentReturned, 8),
		ev(event.SessionEnd, 9),
	}
	for _, in := range kinds {
		tr.emit(t, in)
		got := recv(t, out)
		if got.Kind != in.Kind || got.Tool != in.Tool {
			t.Fatalf("fallback mode dropped %v/%q (got %v/%q)", in.Kind, in.Tool, got.Kind, got.Tool)
		}
	}
}

// TestMuxSilentHooks: a hooks source that is present but has emitted nothing
// (or nothing recently, or is disconnected) must not suppress the transcript.
func TestMuxSilentHooks(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr)

	// Phase 1: hooks never emitted — transcript passes hook-covered kinds.
	tr.emit(t, ev(event.SessionStart, 1))
	if got := recv(t, out); got.Kind != event.SessionStart {
		t.Fatalf("silent hooks must not suppress: got %v", got.Kind)
	}

	// Phase 2: hooks emit — suppression starts.
	hooks.emit(t, ev(event.UserPromptSubmitted, 1))
	if got := recv(t, out); got.Kind != event.UserPromptSubmitted {
		t.Fatalf("want hooks event, got %v", got.Kind)
	}
	tr.emit(t, toolStart("Bash", 2))       // suppressed
	tr.emit(t, ev(event.TokensUpdated, 3)) // marker
	if got := recv(t, out); got.Kind != event.TokensUpdated {
		t.Fatalf("active hooks must suppress transcript ToolStart: got %v", got.Kind)
	}

	// Phase 3: hooks fall silent past the window — fallback resumes.
	clock.advance(hookActiveWindow + time.Second)
	tr.emit(t, toolStart("Bash", 4))
	if got := recv(t, out); got.Kind != event.ToolStart {
		t.Fatalf("stale hooks must stop suppressing: got %v", got.Kind)
	}

	// Phase 4: hooks resume, then disconnect — fallback again.
	hooks.emit(t, ev(event.ToolEnd, 2))
	if got := recv(t, out); got.Kind != event.ToolEnd {
		t.Fatalf("want hooks ToolEnd, got %v", got.Kind)
	}
	hooks.emit(t, ev(event.SourceDisconnected, 3))
	if got := recv(t, out); got.Kind != event.SourceDisconnected {
		t.Fatalf("want SourceDisconnected, got %v", got.Kind)
	}
	tr.emit(t, ev(event.FileEdited, 5))
	if got := recv(t, out); got.Kind != event.FileEdited {
		t.Fatalf("disconnected hooks must stop suppressing: got %v", got.Kind)
	}
}

// TestMuxHooksTodoWriteWithoutTranscript: with no transcript source the hooks
// TodoWrite ToolStart is the only record and must pass.
func TestMuxHooksTodoWriteWithoutTranscript(t *testing.T) {
	hooks := newScripted("hooks")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks)

	hooks.emit(t, toolStart(todoWriteTool, 1))
	got := recv(t, out)
	if got.Kind != event.ToolStart || got.Tool != todoWriteTool {
		t.Fatalf("hooks TodoWrite must pass without a transcript source: got %v/%q", got.Kind, got.Tool)
	}
}

// TestMuxSeqMonotonic: per-source Seqs collide; the mux must reassign one
// strictly increasing global sequence in delivery order.
func TestMuxSeqMonotonic(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	reg := newScripted("registry")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr, reg)

	// Deliberately colliding per-source Seqs (all start at 1).
	emits := []struct {
		src *scriptedSource
		in  event.Event
	}{
		{hooks, ev(event.SessionStart, 1)},
		{tr, ev(event.TokensUpdated, 1)},
		{reg, ev(event.SessionIdle, 1)},
		{hooks, toolStart("Bash", 2)},
		{tr, ev(event.ThinkingEnded, 2)},
		{reg, ev(event.NotificationRaised, 2)},
	}
	var got []event.Event
	for _, e := range emits {
		e.src.emit(t, e.in)
		got = append(got, recv(t, out))
	}
	var last uint64
	for i, g := range got {
		if g.Seq <= last {
			t.Fatalf("event %d: Seq %d not strictly greater than %d", i, g.Seq, last)
		}
		last = g.Seq
	}
	if got[0].Seq != 1 {
		t.Fatalf("global Seq must start at 1, got %d", got[0].Seq)
	}
}

// TestMuxTransportDetailCarriesSourceName: SourceConnected/Disconnected are
// forwarded per source with the source name in Detail.
func TestMuxTransportDetailCarriesSourceName(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr)

	hooks.emit(t, ev(event.SourceConnected, 1))
	got := recv(t, out)
	if got.Kind != event.SourceConnected || got.Detail != "hooks" {
		t.Fatalf("want SourceConnected Detail %q, got %v %q", "hooks", got.Kind, got.Detail)
	}

	in := ev(event.SourceDisconnected, 1)
	in.Detail = "socket gone"
	tr.emit(t, in)
	got = recv(t, out)
	if got.Kind != event.SourceDisconnected || got.Detail != "transcript: socket gone" {
		t.Fatalf("want Detail %q, got %v %q", "transcript: socket gone", got.Kind, got.Detail)
	}
}

// TestMuxRegistryKindsPassWhileHooksActive: SessionIdle and
// NotificationRaised come only from the registry and are never suppressed.
func TestMuxRegistryKindsPassWhileHooksActive(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	reg := newScripted("registry")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr, reg)

	hooks.emit(t, ev(event.SessionStart, 1))
	recv(t, out)

	reg.emit(t, ev(event.SessionIdle, 1))
	if got := recv(t, out); got.Kind != event.SessionIdle {
		t.Fatalf("registry SessionIdle suppressed: got %v", got.Kind)
	}
	reg.emit(t, ev(event.NotificationRaised, 2))
	if got := recv(t, out); got.Kind != event.NotificationRaised {
		t.Fatalf("registry NotificationRaised suppressed: got %v", got.Kind)
	}
}

// TestMuxNoSources: zero (or all-nil) sources is a caller bug, reported as an
// error, never a panic (C9).
func TestMuxNoSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Mux(ctx); err == nil {
		t.Fatal("want error for zero sources")
	}
	if _, err := Mux(ctx, nil, nil); err == nil {
		t.Fatal("want error for all-nil sources")
	}
}

// TestMuxClosesWhenSourcesClose: the output channel closes once every source
// channel has closed.
func TestMuxClosesWhenSourcesClose(t *testing.T) {
	hooks := newScripted("hooks")
	tr := newScripted("transcript")
	clock := &testClock{at: time.Unix(1000, 0)}
	out, _ := startMux(t, clock, hooks, tr)

	close(hooks.ch)
	close(tr.ch)
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("unexpected event after all sources closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mux output never closed")
	}
}
