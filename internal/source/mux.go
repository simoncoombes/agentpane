// Package source provides Mux, the fan-in multiplexer that merges the
// individual event.Source implementations (hooksrc, transcript, registry,
// fake) into the single stream the state machine consumes.
//
// The mux owns two responsibilities beyond plain fan-in:
//
//  0. Ingest flattening (§13.1). Tool text arrives multi-line — a `python3 -c`
//     one-liner, a heredoc, a stack trace — and a newline has zero display
//     width, so a row carrying one silently becomes several physical lines. The
//     mux flattens Target, Says and the display half of Detail as events pass,
//     keeping the exact command in RawTarget, so no draw-time code has to
//     defend the frame and `y` still yanks what the tool really ran.
//
//  1. Seq reassignment. Every source numbers its own events from 1, and the
//     state machine's dedupe is keyed on (AgentID, Kind, Seq) — per-source
//     sequences collide, so the mux assigns one global monotonic Seq at
//     ingest, in the order events reach the consumer.
//
//  2. Double-reporting suppression (DATA-SOURCES §1). Hooks are the primary
//     source and the transcript the fallback: both report the same tool and
//     lifecycle activity. While the hooks source is connected and has emitted
//     recently, transcript events whose Kind duplicates hook coverage are
//     dropped — EXCEPT ToolStart with Tool "TodoWrite", because the
//     transcript is the only channel carrying the todo text in Detail; the
//     hooks-side TodoWrite ToolStart is symmetrically dropped (only when a
//     transcript source is present) so main's call count sees exactly one.
//     Transcript-only kinds (TokensUpdated, ThinkingEnded, PermissionDenied,
//     ErrorRaised, SessionTitled) and registry kinds (SessionIdle,
//     NotificationRaised) are never suppressed. AgentSpawned and AgentStarted
//     are not suppressed either, for the reason spelled out on hookCovered.
//     When hooks are absent, silent, or disconnected, the
//     transcript passes everything — fallback mode.
//
// Sources are discriminated by Source.Name(): "hooks" and "transcript" get
// the special handling above; every other name passes through untouched.
package source

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const (
	hooksName      = "hooks"
	transcriptName = "transcript"
	todoWriteTool  = "TodoWrite"

	// hookActiveWindow is how recently the hooks source must have emitted a
	// (non-transport) event to count as active. Hooks silent for longer flip
	// the mux back to fallback mode, where the transcript passes everything.
	hookActiveWindow = 15 * time.Second

	// outBufferSize absorbs bursts (a seek replay, a transcript catch-up)
	// without stalling source goroutines on the UI's 10fps drain.
	outBufferSize = 256
)

// hookCovered lists the Kinds the hooks source also delivers: while hooks are
// active these are suppressed from the transcript (DATA-SOURCES §1). Kinds
// outside this set always pass, whoever emits them.
//
// AgentSpawned and AgentStarted are deliberately NOT in this set even though
// hooks emit AgentStarted. Hooks cannot name an agent reliably: SubagentStart
// carries agent_id but no description, and the spawning PreToolUse carries the
// description but no agent_id (DATA-SOURCES §3.3). The transcript's sidecar
// join (agent-<id>.meta.json: agentType + description + toolUseId) is the only
// exact one, so suppressing its spawn/start events threw away the only naming
// information the process had and left rows called "agent", "agent-2", ….
// Both kinds are idempotent in the state machine — AgentSpawned only lowers
// spawnedAt, AgentStarted only lifts queued→run, and a name is merged onto an
// existing agent without touching its stations, status, calls, or history —
// so passing the duplicate through enriches instead of double-counting.
var hookCovered = map[event.Kind]bool{
	event.SessionStart:        true,
	event.SessionEnd:          true,
	event.UserPromptSubmitted: true,
	event.AgentReturned:       true,
	event.ToolStart:           true,
	event.ToolEnd:             true,
	event.FileRead:            true,
	event.FileEdited:          true,
	event.CompactStarted:      true,
	event.CompactFinished:     true,
}

// Mux starts every source and returns the merged stream. The output channel
// closes when all source channels have closed (normally on ctx cancel). If
// any source fails to start, everything already started is cancelled and the
// error is returned.
func Mux(ctx context.Context, sources ...event.Source) (<-chan event.Event, error) {
	return mux(ctx, time.Now, sources...)
}

// tagged pairs an event with the name of the source that produced it.
type tagged struct {
	name string
	ev   event.Event
}

// mux is Mux with an injectable clock (deterministic tests).
func mux(ctx context.Context, clock func() time.Time, sources ...event.Source) (<-chan event.Event, error) {
	live := make([]event.Source, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil, errors.New("mux: no sources")
	}

	ctx, cancel := context.WithCancel(ctx)

	st := &muxState{clock: clock}
	type input struct {
		name string
		ch   <-chan event.Event
	}
	inputs := make([]input, 0, len(live))
	for _, s := range live {
		name := s.Name()
		ch, err := s.Events(ctx)
		if err != nil {
			cancel() // release sources that already started
			return nil, err
		}
		switch name {
		case hooksName:
			st.hooksPresent = true
		case transcriptName:
			st.transcriptPresent = true
		}
		inputs = append(inputs, input{name: name, ch: ch})
	}

	raw := make(chan tagged)
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func(in input) {
			defer wg.Done()
			for ev := range in.ch {
				select {
				case raw <- tagged{name: in.name, ev: ev}:
				case <-ctx.Done():
					return
				}
			}
		}(in)
	}
	go func() {
		wg.Wait()
		close(raw)
	}()

	out := make(chan event.Event, outBufferSize)
	go func() {
		defer cancel()
		defer close(out)
		dead := false // consumer gone: keep draining so readers can exit
		for t := range raw {
			ev, ok := st.filter(t.name, t.ev)
			if !ok || dead {
				continue
			}
			st.seq++
			ev.Seq = st.seq
			// Ingest is the one place multi-line tool text is flattened
			// (§13.1): every consumer downstream — the state machine, the
			// inspector log, the debug ring — then receives text that is
			// already one row wide, and the exact bytes survive in RawTarget
			// for `y`.
			ev = event.Flattened(ev)
			select {
			case out <- ev:
			case <-ctx.Done():
				dead = true
			}
		}
	}()
	return out, nil
}

// muxState is the single-goroutine filter state: only the pump touches it, so
// it needs no locking.
type muxState struct {
	clock func() time.Time
	seq   uint64

	hooksPresent      bool
	transcriptPresent bool
	hooksConnected    bool
	lastHookAt        time.Time
}

// hooksActive reports whether hook coverage is currently trustworthy:
// connected and emitting within hookActiveWindow.
func (st *muxState) hooksActive() bool {
	return st.hooksPresent && st.hooksConnected &&
		!st.lastHookAt.IsZero() && st.clock().Sub(st.lastHookAt) <= hookActiveWindow
}

// filter applies the double-reporting rules. It returns the (possibly
// annotated) event and whether it should be forwarded.
func (st *muxState) filter(name string, ev event.Event) (event.Event, bool) {
	// Transport events always pass, annotated with the source name so the
	// UI's `?` overlay and debug log can tell which source flapped.
	if ev.Kind == event.SourceConnected || ev.Kind == event.SourceDisconnected {
		if ev.Detail == "" {
			ev.Detail = name
		} else {
			ev.Detail = name + ": " + ev.Detail
		}
		if name == hooksName {
			st.hooksConnected = ev.Kind == event.SourceConnected
		}
		return ev, true
	}

	switch name {
	case hooksName:
		// Any content event proves the socket is alive and flowing.
		st.hooksConnected = true
		st.lastHookAt = st.clock()
		if ev.Kind == event.ToolStart && ev.Tool == todoWriteTool && st.transcriptPresent {
			// The transcript's TodoWrite ToolStart is the one that carries
			// the todo text; dropping the hooks twin keeps main's call
			// count at exactly one. Without a transcript source the hooks
			// event is the only record, so it passes.
			return ev, false
		}
		return ev, true

	case transcriptName:
		if st.hooksActive() && hookCovered[ev.Kind] {
			if ev.Kind == event.ToolStart && ev.Tool == todoWriteTool {
				return ev, true // only channel with the todo text in Detail
			}
			return ev, false
		}
		return ev, true
	}

	// registry, fake, anything else: pass through untouched.
	return ev, true
}
