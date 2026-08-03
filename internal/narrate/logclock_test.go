package narrate

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// A pane attaches to a session already in progress, so the transcript replays
// spawns and returns that PREDATE the current run. Stamping those against the
// run's start clamped every one to 0:00: a 40-minute-old session rendered six
// entries all at 0:00, in an order that then looked arbitrary. The log's clock
// is the earliest thing the world remembers; the run keeps its own.
func TestPreRunHistoryKeepsItsOrder(t *testing.T) {
	base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	now := base.Add(40 * time.Minute)

	w := state.World{
		Run: &state.Run{StartedAt: now.Add(-19 * time.Second)}, // attached late
		Agents: []state.Agent{
			{ID: "a1", Name: "first agent", Status: state.StatusDone,
				SpawnedAt: base, DoneAt: base.Add(30 * time.Second), LastEventAt: base.Add(30 * time.Second)},
			{ID: "a2", Name: "second agent", Status: state.StatusDone,
				SpawnedAt: base.Add(10 * time.Minute), DoneAt: base.Add(11 * time.Minute),
				LastEventAt: base.Add(11 * time.Minute)},
		},
	}

	r := Narrate(w, Memory{}, now, Options{})
	if len(r.New) < 4 {
		t.Fatalf("want the pre-run history, got %d entries", len(r.New))
	}

	var zeros int
	for i, e := range r.New {
		if e.At == 0 {
			zeros++
		}
		if i > 0 && e.At < r.New[i-1].At {
			t.Errorf("entry %d (%v %q) precedes %v", i, e.At, e.Text, r.New[i-1].At)
		}
	}
	if zeros > 2 {
		t.Errorf("%d of %d entries collapsed to 0:00 — the log clock is still the run clock",
			zeros, len(r.New))
	}

	// The second agent's history must sort after the first's.
	var firstDone, secondSpawn int = -1, -1
	for i, e := range r.New {
		if strings.Contains(e.Text, "first agent") && strings.Contains(e.Text, "returned") {
			firstDone = i
		}
		if strings.Contains(e.Text, "second agent") && strings.Contains(e.Text, "Spawned") {
			secondSpawn = i
		}
	}
	if firstDone >= 0 && secondSpawn >= 0 && firstDone > secondSpawn {
		t.Errorf("the first agent's return sorted after the second's spawn")
	}
}

// A spawn and a return in the SAME second must still read in causal order: a
// phantom subagent is announced and settles inside one second.
func TestSpawnSortsBeforeReturnAtTheSameInstant(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	log := []Entry{{Key: "done:a1", At: 5 * time.Second, Text: "a1 returned."}}
	fresh := []Entry{{Key: "spawn:a1", At: 5 * time.Second, Text: "Spawned a1."}}
	out := mergeEntries(log, fresh)
	if !strings.Contains(out[0].Text, "Spawned") {
		t.Errorf("spawn must precede the return at an equal stamp, got %q then %q",
			out[0].Text, out[1].Text)
	}
	_ = at
}
