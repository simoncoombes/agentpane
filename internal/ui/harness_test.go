package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/state"
)

// demoNow is the canonical §2.10 snapshot instant.
func demoNow() time.Time {
	return fake.Epoch().Add(fake.SnapshotSeconds * time.Second)
}

// demoMachine replays the fake source through the real state machine exactly
// like internal/source/fake/state_integration_test.go, returning the machine
// ticked to the §2.10 snapshot plus every event that was drained (for the
// EventLog).
func demoMachine(t *testing.T) (*state.Machine, []event.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := fake.New(fake.Options{SeekSeconds: fake.SnapshotSeconds})
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	m := state.New(state.DefaultConfig())
	var drained []event.Event
	var count int
drain:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break drain
			}
			m.Apply(ev, ev.At)
			drained = append(drained, ev)
			count++
		case <-time.After(500 * time.Millisecond):
			break drain
		}
	}
	if count < 50 {
		t.Fatalf("suspiciously few events drained: %d", count)
	}
	m.Tick(demoNow())
	return m, drained
}

// demoView builds a UIState with the demo events ingested into the log,
// mirroring what the Model does at runtime.
func demoView(t *testing.T, m *state.Machine, events []event.Event) (*UIState, state.World) {
	t.Helper()
	v := newUIState(testConfig())
	for _, ev := range events {
		v.Logs.Ingest(ev)
	}
	w := m.Snapshot()
	v.assignSlugsInSpawnOrder(w.Agents)
	// Select the ask agent, like the demo's first paint.
	for _, a := range w.Agents {
		if a.Status == state.StatusAsk {
			v.SelID = a.ID
			break
		}
	}
	// §2.10: three sessions exist, so the active header carries the attached
	// session's short id (§3.8a) — mirror the demo wiring.
	for _, s := range fake.DemoSessions() {
		v.Sessions = append(v.Sessions, SessionRow{
			ID: s.ID, ShortID: s.ShortID, Dir: s.Dir, Live: s.Live,
			Idle: s.Idle, Attached: s.Attached, Agents: s.Agents, EndedAgo: s.EndedAgo,
		})
	}
	v.SourceName = "fake"
	v.Version = "test"
	return v, w
}

func testConfig() config.Config {
	return config.Default()
}

// stripANSI removes SGR/OSC sequences for layout-only assertions.
func stripANSI(s string) string {
	out := make([]rune, 0, len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b {
			if i+1 < len(rs) && rs[i+1] == '[' {
				for i++; i < len(rs); i++ {
					r := rs[i]
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
						break
					}
				}
				continue
			}
			if i+1 < len(rs) && rs[i+1] == ']' {
				for i++; i < len(rs); i++ {
					if rs[i] == 0x07 {
						break
					}
					if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '\\' {
						i++
						break
					}
				}
				continue
			}
			continue
		}
		out = append(out, rs[i])
	}
	return string(out)
}

// syntheticWorld builds a World with n subagents in mixed statuses for the
// trunk/scroll tests, without touching the machine.
func syntheticWorld(n int, now time.Time) state.World {
	w := state.World{
		Session:   state.Session{ID: "feedbeef", ShortID: "feed", State: state.SessionActive},
		Main:      state.Main{Todo: "orchestrate", Activity: "fan out", Tokens: 12000, LastEventAt: now.Add(-6 * time.Second)},
		Connected: true,
	}
	prompt := "big refactor"
	w.Run = &state.Run{ID: "r1", Prompt: prompt, StartedAt: now.Add(-4 * time.Minute), Tokens: 100000}
	for i := 0; i < n; i++ {
		a := state.Agent{
			ID:          fmt.Sprintf("agent-%02d", i),
			Name:        fmt.Sprintf("Port the number %d routes", i),
			Type:        "general-purpose",
			Status:      state.StatusRun,
			Station:     1,
			SpawnIndex:  i + 1,
			SpawnedAt:   now.Add(-3 * time.Minute),
			LastEventAt: now.Add(-time.Duration(i%9) * time.Second),
			Tokens:      1000 * (i + 1),
		}
		switch i % 5 {
		case 1:
			a.Status = state.StatusQueued
			a.Station = 0
		case 2:
			a.Status = state.StatusDone
			a.Station = 3
			a.DoneAt = now.Add(-10 * time.Second)
		case 3:
			a.CallHistory = []state.Call{{Tool: "Bash", Target: "make test", Meta: "ok"}}
			a.TotalCalls = 12
		}
		w.Agents = append(w.Agents, a)
	}
	// §3.6 order: sort by rank, stable by spawn.
	for i := 1; i < len(w.Agents); i++ {
		for j := i; j > 0 && w.Agents[j].Status.Rank() < w.Agents[j-1].Status.Rank(); j-- {
			w.Agents[j], w.Agents[j-1] = w.Agents[j-1], w.Agents[j]
		}
	}
	return w
}
