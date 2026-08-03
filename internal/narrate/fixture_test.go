package narrate

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The three §13.2 scenes as hand-built worlds. They are written here rather than
// replayed from internal/source/fake on purpose: this package is pure, and a
// fixture that cannot drift with the demo script is what makes the determinism
// and ladder tests mean something.

func base() time.Time { return time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC) }

func at(sec int) time.Time { return base().Add(time.Duration(sec) * time.Second) }

// snapNow is the instant every fixture is narrated at: 3m28s into the run,
// matching the demo's own snapshot offset closely enough that the prose reads
// like a real mid-run frame.
func snapNow() time.Time { return at(208) }

func agent(id, name string, st state.Status, spawn, last int) state.Agent {
	return state.Agent{
		ID: id, Name: name, NameExact: true, Type: "general-purpose", Status: st,
		SpawnedAt: at(spawn), LastEventAt: at(last), SpawnIndex: spawn,
	}
}

// blockedWorld is the scene the pane exists for: one outstanding ask, one agent
// gone quiet, a file collision, a queued agent parked behind the prompt.
func blockedWorld() state.World {
	askAgent := agent("a1", "fix the TS2345 fallout in api handlers", state.StatusAsk, 4, 156)
	askAgent.Retries = 2
	askAgent.Tokens = 44000
	askAgent.LinesAdd, askAgent.LinesDel = 35, 6
	askAgent.EditedFiles = []string{"api/types.ts", "api/handlers.ts"}
	askAgent.LastError = "TS2345 at handlers.ts:88"

	quiet := agent("a2", "check the typecheck works", state.StatusStuck, 4, 38)
	quiet.Tokens = 33000
	quiet.Station = 2

	live1 := agent("a3", "audit the users schema", state.StatusRun, 4, 205)
	live1.Tokens = 52000
	live1.EditedFiles = []string{"db/schema.ts"}
	live2 := agent("a4", "write the email index", state.StatusRun, 4, 207)
	live2.Tokens = 86000
	live2.EditedFiles = []string{"db/schema.ts", "db/migrations/0042.sql"}
	live2.Verify = state.VerifyOK
	live2.Verified = true

	watch := agent("a5", "run the auth tests", state.StatusRun, 10, 12)
	watch.OpenCall = &state.OpenCall{Tool: "Bash", Target: "pnpm test --watch", Since: at(16)}

	queued := agent("a6", "document the constraint", state.StatusQueued, 11, 11)
	back := agent("a7", "lint and format", state.StatusDone, 12, 162)
	back.DoneAt = at(162)
	back.LinesAdd, back.LinesDel = 12, 12
	back.EditedFiles = []string{"README.md"}
	mate := agent("a8", "reviewer", state.StatusTeammate, 13, 13)
	mate.Teammate = true

	return state.World{
		Session: state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionActive},
		Main: state.Main{
			Title: "Add unique email index", Activity: "write the migration",
			Tokens: 112000, LastEventAt: at(203),
		},
		Agents: []state.Agent{askAgent, quiet, live1, live2, watch, queued, back, mate},
		Quiet: []state.QuietAgent{
			{ID: "q1", Type: "general-purpose", Status: state.StatusDone},
			{ID: "q2", Type: "general-purpose", Status: state.StatusDone},
			{ID: "q3", Type: "explore", Status: state.StatusDone},
		},
		Asks: []state.Ask{{
			Key: "Bash|rm -rf node_modules/.cache", AgentID: "a1",
			Description: "fix the TS2345 fallout in api handlers", Tool: "Bash",
			Command: "rm -rf node_modules/.cache && pnpm rebuild", Kind: "command",
			RaisedAt: at(156), DupeCount: 3, FoldedGhosts: 2,
		}},
		Contentions: []state.Contention{{
			Path: "db/schema.ts", AgentIDs: []string{"a3", "a4"},
			AgentNames: []string{"audit the users schema", "write the email index"},
			DetectedAt: at(182),
		}},
		Run:       &state.Run{ID: "r1", StartedAt: base(), Tokens: 412000},
		Connected: true,
	}
}

// riskyWorld is the ask answered, the quiet agent still quiet, the collision
// still live: nothing to do, plenty to know.
func riskyWorld() state.World {
	w := blockedWorld()
	w.Asks = nil
	agents := append([]state.Agent(nil), w.Agents...)
	agents[0].Status = state.StatusRun
	agents[0].LastEventAt = at(204)
	agents[0].Ask = nil
	w.Agents = agents
	return w
}

// clearWorld is everything back, the collision resolved, and the agent the
// narrator wrote off returned anyway.
func clearWorld() state.World {
	w := riskyWorld()
	agents := append([]state.Agent(nil), w.Agents...)
	for i := range agents {
		if agents[i].Teammate {
			continue
		}
		agents[i].Status = state.StatusDone
		agents[i].DoneAt = at(200 + i)
		agents[i].Station = 3
		agents[i].OpenCall = nil
	}
	w.Agents = agents
	w.Contentions[0].DetectedAt = at(182)
	return w
}

func emptyWorld() state.World {
	return state.World{
		Session:   state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionActive},
		Connected: true,
	}
}

// scenes is what the matrix tests walk.
func scenes() map[string]state.World {
	return map[string]state.World{
		"blocked": blockedWorld(),
		"risky":   riskyWorld(),
		"clear":   clearWorld(),
		"empty":   emptyWorld(),
	}
}

// testNames is a deterministic stand-in for ui's slug table.
func testNames(a state.Agent) string {
	if a.Name == "" {
		return a.ID
	}
	f := strings.Fields(a.Name)
	keep := make([]string, 0, 3)
	for _, w := range f {
		switch w {
		case "the", "a", "an", "in", "of", "to":
			continue
		}
		keep = append(keep, strings.ToLower(w))
		if len(keep) == 3 {
			break
		}
	}
	return strings.Join(keep, "-")
}

func opt() Options { return Options{Name: testNames} }

// texts flattens a standup into plain sentences.
func texts(st Standup) []string {
	out := make([]string, 0, len(st.Lines)+1)
	for _, l := range st.All() {
		out = append(out, l.Text)
	}
	return out
}

func joined(st Standup) string { return strings.Join(texts(st), "\n") }
