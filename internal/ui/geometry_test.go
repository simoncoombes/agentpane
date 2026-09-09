package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// Geometry invariants for the frame after the regions below the tree were
// removed. The tree now owns every row between the header and the inspector, so
// what used to be a negotiation is a subtraction — and these are the two
// properties that negotiation existed to protect.

// bandWorld builds a world carrying every §3.7 shape at once: four asks so the
// cap bites, a deduped-and-folded oldest ask, a resolving second ask, and a
// stalled agent behind them.
func bandWorld(now time.Time) state.World {
	agent := func(id, name string, st state.Status, last time.Time) state.Agent {
		return state.Agent{
			ID: id, Name: name, NameExact: true, Status: st, Station: 2,
			SpawnIndex: 1, SpawnedAt: now.Add(-5 * time.Minute), LastEventAt: last,
			ActivityLine: "working", Tokens: 1000,
		}
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Activity: "orchestrating", LastEventAt: now, Tokens: 2000},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-5 * time.Minute), Tokens: 9000},
		Connected: true,
		Agents: []state.Agent{
			agent("a1", "Clear the stale type cache", state.StatusAsk, now.Add(-52*time.Second)),
			agent("a2", "Write the email index migration", state.StatusAsk, now.Add(-30*time.Second)),
			agent("a3", "Audit the users schema", state.StatusAsk, now.Add(-20*time.Second)),
			agent("a4", "Run the auth test suite", state.StatusAsk, now.Add(-10*time.Second)),
			agent("a5", "Typecheck the worker package", state.StatusStuck, now.Add(-170*time.Second)),
		},
		Asks: []state.Ask{
			{Key: "k1", AgentID: "a1", Description: "Clear the stale type cache", Tool: "Bash",
				Command: "rm -rf node_modules/.cache && pnpm rebuild", Kind: "command",
				RaisedAt: now.Add(-52 * time.Second), DupeCount: 3, FoldedGhosts: 2},
			{Key: "k2", AgentID: "a2", Description: "Write the email index migration", Tool: "Bash",
				Command: "psql -f db/migrations/0042.sql", Kind: "command",
				RaisedAt: now.Add(-30 * time.Second), DupeCount: 1, Resolving: true},
			{Key: "k3", AgentID: "a3", Description: "Audit the users schema", Tool: "Bash",
				Command: "grep -rn createUser src", Kind: "command",
				RaisedAt: now.Add(-20 * time.Second), DupeCount: 1},
			{Key: "k4", AgentID: "a4", Description: "Run the auth test suite", Tool: "Bash",
				Command: "pnpm test --filter auth", Kind: "command",
				RaisedAt: now.Add(-10 * time.Second), DupeCount: 1},
		},
	}
	return w
}

// oneAskWorld is the simplest world that can state an ask twice: one agent, one
// ask, no dedupe count, no folded ghosts and nothing stalled behind it. It is the
// world where the band body is short enough that the narrator's first sentence is
// on screen next to it in every mode that draws prose.
func oneAskWorld(now time.Time) state.World {
	a := state.Agent{
		ID: "a1", Name: "Write the email index migration", NameExact: true,
		Status: state.StatusAsk, Station: 2, SpawnIndex: 1,
		SpawnedAt: now.Add(-5 * time.Minute), LastEventAt: now.Add(-52 * time.Second),
		ActivityLine: "blocked", Tokens: 1000,
	}
	return state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Title: "Add unique email index", LastEventAt: now, Tokens: 2000},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-5 * time.Minute), Tokens: 9000},
		Connected: true,
		Agents:    []state.Agent{a},
		Asks: []state.Ask{{
			Key: "k1", AgentID: "a1", Description: a.Name, Tool: "Bash",
			Command: "psql -f db/migrations/0042.sql", Kind: "command",
			RaisedAt: now.Add(-52 * time.Second), DupeCount: 1,
		}},
	}
}

// askAndStallWorld is one outstanding ask with a stalled agent behind it: the
// shape §3.7 has to fit two different sets of keys into one action row.
func askAndStallWorld(now time.Time) state.World {
	w := oneAskWorld(now)
	w.Agents = append(w.Agents, state.Agent{
		ID: "a2", Name: "Typecheck the worker package", NameExact: true,
		Status: state.StatusStuck, Station: 2, SpawnIndex: 2,
		SpawnedAt: now.Add(-4 * time.Minute), LastEventAt: now.Add(-170 * time.Second),
		ActivityLine: "no call open",
	})
	return w
}

// viewOf is a fresh UIState with the world's slugs assigned, as the Model does on
// its first paint.
// viewOf builds the view state for a hand-made world, including the event-log
// lines a real session would already have for its outstanding asks. The log is
// what the inspector reads, and since the alert band was removed the inspector
// is the only surface that states an ask's verbatim command — so a world whose
// log is empty is a world that cannot print one at all.
func viewOf(w state.World) *UIState {
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	for _, ask := range w.Asks {
		v.Logs.Ingest(event.Event{
			Kind:    event.PermissionRequested,
			AgentID: ask.AgentID,
			Target:  ask.Command,
			Ask:     &event.AskPayload{Command: ask.Command},
		})
	}
	return v
}

func squash(plain string) string { return strings.Join(strings.Fields(plain), " ") }

// treeTop is the y of the tree's first row. emitMain always runs first, so main's
// own row IS the top of the tree.
func treeTop(meta *frameMeta) int {
	for y, id := range meta.lineIDs {
		if id == event.MainAgentID {
			return y
		}
	}
	return -1
}

// The footer carries the toasts and every key hint, so it may never be clipped:
// a pane whose keys have scrolled away is a pane you cannot get out of. The
// inspector is the one fixed-height region that still competes for these rows,
// and a short pane with it open is exactly where the pad used to go negative and
// take the footer with it.
func TestFooterSurvivesEveryHeight(t *testing.T) {
	now := demoNow()
	dm, events := demoMachine(t)

	worlds := map[string]func() (*UIState, state.World){
		"main-only": func() (*UIState, state.World) {
			v := newUIState(testConfig())
			w := state.World{
				Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
				Main:      state.Main{Activity: "thinking", LastEventAt: now, Tokens: 900},
				Run:       &state.Run{ID: "r", StartedAt: now.Add(-time.Minute)},
				Connected: true,
			}
			return v, w
		},
		"main-only-with-an-ask": func() (*UIState, state.World) {
			v := newUIState(testConfig())
			w := bandWorld(now)
			w.Agents = w.Agents[:1]
			w.Asks = w.Asks[:1]
			v.assignSlugsInSpawnOrder(w.Agents)
			return v, w
		},
		"eight-agents": func() (*UIState, state.World) {
			return demoView(t, dm, events)
		},
	}
	for name, build := range worlds {
		for _, insp := range []bool{false, true} {
			for rows := 12; rows <= 50; rows++ {
				v, w := build()
				v.InspectorOpen = insp
				clock := now
				if name == "eight-agents" {
					clock = demoNow()
				}
				frame, _ := renderFrame(w, v, 64, rows, clock, NewPalette(2, false))
				lines := strings.Split(frame, "\n")
				if len(lines) != rows {
					t.Fatalf("%s/insp=%v/%d: %d lines", name, insp, rows, len(lines))
				}
				foot := stripANSI(lines[rows-1])
				if !strings.Contains(foot, "t tokens") {
					t.Errorf("%s/insp=%v/%d: footer clipped, last line = %q",
						name, insp, rows, foot)
				}
			}
		}
	}
}

// §13.4: the tree's first row does not move when the scene changes, so an ask
// arriving or clearing cannot shift a single row under the reader. With the
// regions below the tree gone, nothing fixed-height sits above it either, so the
// invariant is now held by construction — this pins it there.
func TestTreeTopIsInvariantAcrossScenes(t *testing.T) {
	now := demoNow()
	dm, events := demoMachine(t)

	scenes := map[string]func() (*UIState, state.World){
		"every-alert-shape": func() (*UIState, state.World) {
			w := bandWorld(now)
			v := newUIState(testConfig())
			v.assignSlugsInSpawnOrder(w.Agents)
			return v, w
		},
		"no-alerts": func() (*UIState, state.World) {
			w := bandWorld(now)
			w.Asks = nil
			for i := range w.Agents {
				w.Agents[i].Status = state.StatusRun
			}
			v := newUIState(testConfig())
			v.assignSlugsInSpawnOrder(w.Agents)
			return v, w
		},
		"demo": func() (*UIState, state.World) {
			return demoView(t, dm, events)
		},
	}
	for _, rows := range []int{20, 30, 44} {
		want := -1
		for name, build := range scenes {
			v, w := build()
			clock := now
			if name == "demo" {
				clock = demoNow()
			}
			_, meta := renderFrame(w, v, 64, rows, clock, NewPalette(2, false))
			got := treeTop(meta)
			if got < 0 {
				t.Fatalf("rows=%d %s: no tree row in the frame", rows, name)
			}
			if want < 0 {
				want = got
				continue
			}
			if got != want {
				t.Errorf("rows=%d: tree top moved to y=%d (was %d) at %s", rows, got, want, name)
			}
		}
	}
}
