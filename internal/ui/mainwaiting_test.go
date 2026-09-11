package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// mainWorld builds a world with main plus the given agents and asks.
func mainWorld(now time.Time, todo, activity string, agents []state.Agent, asks int) state.World {
	w := state.World{
		Main: state.Main{
			Title: "Add unique email index", Todo: todo, Activity: activity,
			Tokens: 112000, LastEventAt: now.Add(-6 * time.Second),
		},
		Agents:    agents,
		Connected: true,
	}
	for i := 0; i < asks; i++ {
		w.Asks = append(w.Asks, state.Ask{
			Key: "k", AgentID: "ask", Tool: "Bash", Command: "rm -rf .cache",
			RaisedAt: now.Add(-52 * time.Second), DupeCount: 1,
		})
	}
	return w
}

func agentIn(id string, st state.Status) state.Agent {
	return state.Agent{ID: id, Name: "Do the " + id + " work", Status: st, SpawnIndex: 1}
}

// TestMainWaitingPrecedence is §13.3 Q7: main's line 2 is what main is WAITING
// ON — the blocked-on summary, else the current todo, else the current activity.
func TestMainWaitingPrecedence(t *testing.T) {
	now := colNow()
	const activity = "Edit db/migrations/0042.sql"
	cases := []struct {
		name     string
		todo     string
		activity string
		agents   []state.Agent
		asks     int
		want     string
	}{
		{
			name:     "blocked beats a todo",
			todo:     "write the migration",
			activity: activity,
			agents:   []state.Agent{agentIn("ask", state.StatusAsk), agentIn("docs", state.StatusQueued)},
			asks:     1,
			want:     "holding 2 agents · waiting on you",
		},
		{
			name:     "an ask against main alone still names the blocker",
			activity: activity,
			agents:   []state.Agent{agentIn("live", state.StatusRun)},
			asks:     1,
			want:     "waiting on you",
		},
		{
			name:     "one held agent is singular",
			activity: activity,
			agents:   []state.Agent{agentIn("docs", state.StatusQueued)},
			want:     "holding 1 agent",
		},
		{
			name:     "no block: the todo",
			todo:     "write the migration",
			activity: activity,
			agents:   []state.Agent{agentIn("live", state.StatusRun)},
			want:     "write the migration",
		},
		{
			// Live work outranks whatever main last said: with a fan-out
			// running, the idle-prompt Notification text ("Claude is waiting
			// for your input") would otherwise contradict the tree below it.
			name:     "no block and no todo: the agents still running",
			activity: "Claude is waiting for your input",
			agents:   []state.Agent{agentIn("live", state.StatusRun), agentIn("l2", state.StatusRun)},
			want:     "waiting on 2 agents",
		},
		{
			name:     "no block, no todo, nothing running: the activity",
			activity: activity,
			agents:   []state.Agent{agentIn("done", state.StatusDone)},
			want:     activity,
		},
		{
			name: "nothing at all: no line",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := mainWorld(now, c.todo, c.activity, c.agents, c.asks)
			if got := mainWaiting(w, w.Agents); got != c.want {
				t.Errorf("main line 2 = %q, want %q", got, c.want)
			}
		})
	}
}

// The summary is derived from the world, never a literal: the count follows the
// agents, and it disappears when nothing is blocked.
func TestBlockedSummaryIsDerived(t *testing.T) {
	now := colNow()
	held := []state.Agent{
		agentIn("ask", state.StatusAsk),
		agentIn("q1", state.StatusQueued),
		agentIn("q2", state.StatusQueued),
		agentIn("live", state.StatusRun),
	}
	w := mainWorld(now, "", "", held, 1)
	if got := blockedOn(w, w.Agents); got != "holding 3 agents · waiting on you" {
		t.Errorf("summary = %q, want three held agents counted", got)
	}
	// The ask is answered: the same agents, no outstanding ask.
	w.Asks = nil
	if got := blockedOn(w, w.Agents); got != "holding 3 agents" {
		t.Errorf("summary = %q, want the holding half only", got)
	}
	// Everything running or returned: nothing is blocked, so there is no line.
	for i := range w.Agents {
		w.Agents[i].Status = state.StatusRun
	}
	if got := blockedOn(w, w.Agents); got != "" {
		t.Errorf("summary = %q, want none when nothing is blocked", got)
	}
}

// The line reaches the frame, on main's second row, with the trunk in front of
// it — the geometry §3.2 requires.
func TestMainWaitingReachesTheFrame(t *testing.T) {
	now := colNow()
	w := mainWorld(now, "write the migration", "Edit 0042.sql",
		[]state.Agent{agentIn("ask", state.StatusAsk), agentIn("docs", state.StatusQueued)}, 1)
	w.Run = &state.Run{ID: "r1", StartedAt: now.Add(-4 * time.Minute), Tokens: 412000}
	v := newUIState(testConfig())
	frame, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, "│ holding 2 agents · waiting on you") {
		t.Errorf("main's line 2 is not the blocked-on summary:\n%s", plain)
	}
	if strings.Contains(plain, "write the migration") {
		t.Errorf("the todo outranked the block:\n%s", plain)
	}
}
