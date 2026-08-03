package state

import (
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// TestQuietPhantomAgentsSuppressed: Claude Code emits SubagentStart/Stop for
// entities that run nothing and write no transcript. Once it is clear they are
// empty they must not hold rows, and they must not vanish either — the snapshot
// counts them.
func TestQuietPhantomAgentsSuppressed(t *testing.T) {
	f := newFix()
	for i := 0; i < 40; i++ {
		id := phantomID(i)
		f.apply(time.Duration(i)*time.Millisecond, event.AgentStarted, id, withType("general-purpose"))
		f.apply(time.Duration(i)*time.Millisecond, event.AgentReturned, id, withType("general-purpose"))
	}
	// §13.1: not inside stuck_after of the spawn. At this instant nothing
	// distinguishes these from forty agents that are about to do real work.
	if w := f.m.Snapshot(); len(w.Quiet) != 0 {
		t.Fatalf("suppressed %d agents in their first breath, want 0", len(w.Quiet))
	}
	f.tick(DefaultConfig().StuckAfter + time.Second)
	w := f.m.Snapshot()
	if len(w.Agents) != 0 {
		t.Fatalf("phantoms rendered %d rows, want 0", len(w.Agents))
	}
	if len(w.Quiet) != 40 {
		t.Fatalf("suppressed count = %d, want 40 — they must be reported, not dropped", len(w.Quiet))
	}
	for _, q := range w.Quiet {
		if q.Status != StatusDone {
			t.Errorf("quiet agent %s: status %v, want done", q.ID, q.Status)
		}
		if q.Type != "general-purpose" {
			t.Errorf("quiet agent %s lost its agent_type: %q", q.ID, q.Type)
		}
	}
	// Later still: a settled phantom stays suppressed, and no derived alert was
	// ever raised about one (a StallDetected would put it in the band).
	out := f.tick(10 * time.Minute)
	if hasKind(out, event.StallDetected) {
		t.Error("a phantom stalled: an empty agent must never reach the band")
	}
	if w := f.m.Snapshot(); len(w.Agents) != 0 {
		t.Fatalf("a settled phantom earned a row later on: %d rows", len(w.Agents))
	}
}

// TestQuietFirstBreathIsNeverSuppressed is §13.1(c) on its own: an agent that
// has just been announced is indistinguishable from one that is about to work,
// so it keeps its row until silence past stuck_after makes the absence of
// activity evidence rather than timing.
func TestQuietFirstBreathIsNeverSuppressed(t *testing.T) {
	stuck := DefaultConfig().StuckAfter
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withType("Explore"))

	for _, at := range []time.Duration{0, time.Second, stuck / 2, stuck - time.Second} {
		f.tick(at)
		w := f.m.Snapshot()
		if agentByID(w, "a1") == nil {
			t.Fatalf("at %s the agent was hidden inside its first breath (quiet: %+v)", at, w.Quiet)
		}
		if len(w.Quiet) != 0 {
			t.Fatalf("at %s it was counted as suppressed: %+v", at, w.Quiet)
		}
	}
	// The threshold arrives with nothing recorded: now it is a phantom.
	f.tick(stuck)
	w := f.m.Snapshot()
	if len(w.Agents) != 0 {
		t.Fatalf("still rendering an agent that recorded nothing past stuck_after: %+v", w.Agents)
	}
	if q := quietByID(w, "a1"); q == nil {
		t.Fatalf("it was dropped instead of counted: %+v", w.Quiet)
	}

	// One real tool call inside the first breath earns the row for good.
	g := newFix()
	g.apply(0, event.AgentStarted, "b1", withType("Explore"))
	g.apply(time.Second, event.ToolStart, "b1", withTool("Bash", "rg acl"))
	g.tick(10 * time.Minute)
	if w := g.m.Snapshot(); agentByID(w, "b1") == nil || len(w.Quiet) != 0 {
		t.Fatalf("an agent that did something lost its row: agents=%+v quiet=%+v", w.Agents, w.Quiet)
	}
}

// TestQuietEarnsARow enumerates every way an agent earns its row.
func TestQuietEarnsARow(t *testing.T) {
	cases := []struct {
		name string
		mut  func(f *fixture, id string)
	}{
		{"a name", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id, withName("Explore ACL in unstructured-ingest"))
		}},
		{"a spawn record", func(f *fixture, id string) {
			f.apply(0, event.AgentSpawned, id)
			f.apply(0, event.AgentStarted, id)
		}},
		{"a tool call", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.ToolStart, id, withTool("Bash", "go test ./..."))
		}},
		{"a file read", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.FileRead, id, withTool("Read", "/tmp/x.go"))
		}},
		{"an edit", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.FileEdited, id, withTool("Edit", "/tmp/x.go"), withLines(3, 1))
		}},
		{"tokens", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.TokensUpdated, id, withTokens(120, true))
		}},
		{"a permission ask", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.PermissionRequested, id, withTool("Bash", "rm -rf x"), withAsk("command", "rm -rf x"))
		}},
		{"an error", func(f *fixture, id string) {
			f.apply(0, event.AgentStarted, id)
			f.apply(0, event.ErrorRaised, id, withErr("boom"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFix()
			tc.mut(f, "a1")
			w := f.m.Snapshot()
			if agentByID(w, "a1") == nil {
				t.Fatalf("%s did not earn a row (quiet: %+v)", tc.name, w.Quiet)
			}
			if len(w.Quiet) != 0 {
				t.Errorf("agent counted as suppressed as well: %+v", w.Quiet)
			}
		})
	}
}

// TestQuietLongLivedPhantomNeverRenders: being alive is not work. A long-lived
// phantom (session 6d53f05d had four, each ~90s, no tool, no tokens, no
// transcript) holds a row only for its first breath (§13.1) and never earns
// one — outliving a timer is not evidence, so the row does not come back and
// nothing is derived about it.
func TestQuietLongLivedPhantomNeverRenders(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withType("Explore"))
	for _, at := range []time.Duration{
		DefaultConfig().StuckAfter, 60 * time.Second, 90 * time.Second,
	} {
		f.tick(at)
		w := f.m.Snapshot()
		if len(w.Agents) != 0 {
			t.Fatalf("at %s a phantom took a row: %+v", at, w.Agents)
		}
		if q := quietByID(w, "a1"); q == nil {
			t.Fatalf("at %s the phantom was dropped instead of counted", at)
		} else if q.Type != "Explore" {
			t.Errorf("at %s the quiet entry lost its agent_type: %q", at, q.Type)
		}
	}
	// It settles: still no row, so there is no row to lose.
	f.apply(95*time.Second, event.AgentReturned, "a1", withDetail("all done"))
	w := f.m.Snapshot()
	if len(w.Agents) != 0 {
		t.Fatalf("a settled phantom took a row: %+v", w.Agents)
	}
	if len(w.Quiet) != 1 {
		t.Fatalf("quiet = %+v, want the one phantom still reported", w.Quiet)
	}
}

// TestQuietEarnedRowIsLatched: once an agent has earned its row it keeps it for
// the rest of the run, however quiet it goes. The verified failure was rows=1 →
// AgentReturned → rows=0: a row the user was watching disappeared.
func TestQuietEarnedRowIsLatched(t *testing.T) {
	// Earned by an ask, which is then granted — the ask payload clears, and
	// nothing else about this agent was ever observed.
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withType("Explore"))
	f.apply(time.Second, event.PermissionRequested, "a1",
		withTool("Bash", "rm -rf x"), withAsk("command", "rm -rf x"))
	if w := f.m.Snapshot(); agentByID(w, "a1") == nil {
		t.Fatal("an asking agent never got a row")
	}
	f.apply(2*time.Second, event.PermissionDenied, "a1")
	f.apply(3*time.Second, event.AgentReturned, "a1", withDetail("stopped"))
	f.tick(90 * time.Second)
	w := f.m.Snapshot()
	if agentByID(w, "a1") == nil {
		t.Fatalf("the row vanished after the agent settled (quiet: %+v)", w.Quiet)
	}
	if len(w.Quiet) != 0 {
		t.Errorf("an agent with a row was also counted as suppressed: %+v", w.Quiet)
	}

	// Earned by a stall detected in Tick, which then clears.
	g := newFix()
	g.apply(0, event.AgentStarted, "b1", withType("Explore"))
	g.apply(0, event.ToolStart, "b1", withTool("Bash", "sleep 600"))
	g.tick(2 * time.Minute) // silence past StuckAfter
	if w := g.m.Snapshot(); agentByID(w, "b1") == nil {
		t.Fatal("a stalled agent never got a row")
	}
	g.apply(3*time.Minute, event.AgentReturned, "b1")
	if w := g.m.Snapshot(); agentByID(w, "b1") == nil {
		t.Fatalf("the stalled agent's row vanished on settle (quiet: %+v)", w.Quiet)
	}
}

// TestQuietNameArrivesLate: the transcript's sidecar lands after the hooks'
// SubagentStart. The name must merge onto the existing agent without resetting
// anything it has already been observed doing.
func TestQuietNameArrivesLate(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentStarted, "a1", withType("Explore"))
	f.apply(time.Second, event.ToolStart, "a1", withTool("Bash", "rg acl"))
	f.apply(2*time.Second, event.ToolEnd, "a1", withTool("Bash", "rg acl"))
	f.apply(3*time.Second, event.FileEdited, "a1", withTool("Edit", "/tmp/acl.py"), withLines(4, 2))

	before := agentByID(f.m.Snapshot(), "a1")
	if before == nil {
		t.Fatal("working agent missing before the name arrived")
	}
	if before.Name != "" {
		t.Fatalf("Name = %q, want empty until a source supplies one", before.Name)
	}

	// The transcript's AgentSpawned, carrying the sidecar's description.
	f.apply(4*time.Second, event.AgentSpawned, "a1",
		withName("Explore ACL in unstructured-ingest"), withType("Explore"))

	after := agentByID(f.m.Snapshot(), "a1")
	if after == nil {
		t.Fatal("agent disappeared when its name arrived")
	}
	if after.Name != "Explore ACL in unstructured-ingest" {
		t.Fatalf("Name = %q, want the sidecar description", after.Name)
	}
	if after.Station != before.Station {
		t.Errorf("station regressed: %d → %d", before.Station, after.Station)
	}
	if after.Status != before.Status {
		t.Errorf("status changed on enrichment: %v → %v", before.Status, after.Status)
	}
	if after.TotalCalls != before.TotalCalls {
		t.Errorf("call history reset: %d → %d", before.TotalCalls, after.TotalCalls)
	}
	if after.LinesAdd != before.LinesAdd || after.LinesDel != before.LinesDel {
		t.Errorf("diff stats reset: +%d −%d → +%d −%d",
			before.LinesAdd, before.LinesDel, after.LinesAdd, after.LinesDel)
	}
}

// TestQuietTeammatesAndAsksNeverSuppressed: the two classes that must always
// be visible.
func TestQuietTeammatesAndAsksNeverSuppressed(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentSpawned, "teammate:reviewer", withType("teammate"), withName("reviewer"))
	f.apply(0, event.AgentStarted, "a1")
	f.apply(0, event.PermissionRequested, "a1", withTool("Bash", "rm -rf /"), withAsk("command", "rm -rf /"))
	w := f.m.Snapshot()
	if len(w.Quiet) != 0 {
		t.Fatalf("suppressed something that must always show: %+v", w.Quiet)
	}
	if len(w.Agents) != 2 {
		t.Fatalf("rows = %d, want teammate + asking agent", len(w.Agents))
	}
}

// TestQuietExcludedFromRunSummary: a run's per-agent bars must not carry forty
// empty phantoms either.
func TestQuietExcludedFromRunSummary(t *testing.T) {
	f := newFix()
	f.apply(0, event.SessionStart, event.MainAgentID)
	f.apply(0, event.UserPromptSubmitted, event.MainAgentID, withDetail("explore the repo"))
	f.spawnRun(time.Second, "a1")
	f.apply(2*time.Second, event.ToolStart, "a1", withTool("Bash", "rg acl"))
	for i := 0; i < 5; i++ {
		id := phantomID(i)
		f.apply(2*time.Second, event.AgentStarted, id)
		f.apply(2*time.Second, event.AgentReturned, id)
	}
	f.apply(3*time.Second, event.AgentReturned, "a1")
	f.apply(4*time.Second, event.SessionIdle, "")
	w := f.m.Snapshot()
	if w.LastRun == nil {
		t.Fatal("run never ended")
	}
	if n := len(w.LastRun.Agents); n != 1 {
		t.Fatalf("run summary lists %d agents, want only the one that did something", n)
	}
}

func phantomID(i int) string {
	const hex = "0123456789abcdef"
	out := []byte("f0000000000000000")
	out[15] = hex[(i>>4)&0xf]
	out[16] = hex[i&0xf]
	return string(out)
}

// A final message alone does NOT earn a row. SubagentStop always carries
// last_assistant_message, so treating it as work let every long-lived phantom
// through: live session 6d53f05d rendered 4 unnamed rows beside its 5 real
// named subagents, each having run ~90s while touching no tool, spending no
// tokens, and writing no transcript anywhere on disk. They are reported in
// World.Quiet instead (§1.5), never dropped.
func TestFinalMessageAloneIsQuiet(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentStarted, "ghost")
	f.apply(0, event.AgentReturned, "ghost", withDetail("found it in acl.py"))
	f.tick(DefaultConfig().StuckAfter + time.Second) // past the §13.1 first breath
	w := f.m.Snapshot()
	if len(w.Agents) != 0 {
		t.Fatalf("a message-only agent must not take a row, got %d", len(w.Agents))
	}
	if len(w.Quiet) != 1 || w.Quiet[0].ID != "ghost" {
		t.Fatalf("it must still be reported as quiet, got %+v", w.Quiet)
	}
	// A named agent that returns a message keeps its row.
	f.apply(0, event.AgentSpawned, "real", withName("Explore ACL"))
	f.apply(0, event.AgentStarted, "real")
	f.apply(0, event.AgentReturned, "real", withDetail("done"))
	w = f.m.Snapshot()
	if len(w.Agents) != 1 || w.Agents[0].ID != "real" {
		t.Fatalf("a named agent must keep its row, got %+v", w.Agents)
	}
}
