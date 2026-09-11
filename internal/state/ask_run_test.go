package state

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const rebuildCmd = "rm -rf node_modules/.cache && pnpm rebuild"

// raiseTestAsk puts a1 into ask on the canonical §2.10 command.
func raiseTestAsk(f *fixture) {
	f.spawnRun(0, "a1")
	f.apply(1*time.Second, event.ToolStart, "a1", withTool("Bash", rebuildCmd))
	f.apply(2*time.Second, event.PermissionRequested, "a1", withTool("Bash", ""), withAsk("command", rebuildCmd))
}

// TestAskDedupeGhostFolding covers §3.7.7 with the DATA-SOURCES-captured
// pattern: a background subagent retrying the blocked action under fresh
// agent ids while the prompt sits unanswered.
func TestAskDedupeGhostFolding(t *testing.T) {
	f := newFix()
	f.apply(0, event.UserPromptSubmitted, "", withDetail("refactor auth"))
	raiseTestAsk(f)

	w := f.m.Snapshot()
	if len(w.Asks) != 1 || w.Asks[0].DupeCount != 1 || w.Asks[0].Command != rebuildCmd {
		t.Fatalf("initial ask = %+v", w.Asks)
	}
	if got := agentByID(w, "a1").Status; got != StatusAsk {
		t.Fatalf("asker status = %v, want ask", got)
	}

	// Ghost 1 already has tree rows when its duplicate ask arrives.
	f.spawnRun(10*time.Second, "g1")
	out := f.apply(11*time.Second, event.PermissionRequested, "g1",
		withTool("Bash", ""), withAsk("command", "rm  -rf   node_modules/.cache &&  pnpm rebuild"))
	if !hasKind(out, event.GhostAgentFolded) || !hasKind(out, event.AskDeduped) {
		t.Fatalf("dup ask from a different agent: got %v, want GhostAgentFolded + AskDeduped", out)
	}
	w = f.m.Snapshot()
	if len(w.Asks) != 1 || w.Asks[0].DupeCount != 2 || w.Asks[0].FoldedGhosts != 1 {
		t.Fatalf("ask after ghost 1 = %+v", w.Asks[0])
	}
	if agentByID(w, "g1") != nil {
		t.Fatal("ghost g1 still renders as a tree row")
	}
	if agentByID(w, "a1") == nil {
		t.Fatal("original asker lost its row")
	}
	if !w.Asks[0].RaisedAt.Equal(f.at(2 * time.Second)) {
		t.Fatalf("dedupe reset the wait timer: %v", w.Asks[0].RaisedAt)
	}

	// Ghost 2, same shape.
	f.spawnRun(20*time.Second, "g2")
	f.apply(21*time.Second, event.PermissionRequested, "g2", withTool("Bash", ""), withAsk("command", rebuildCmd))
	w = f.m.Snapshot()
	if w.Asks[0].DupeCount != 3 || w.Asks[0].FoldedGhosts != 2 {
		t.Fatalf("ask after ghost 2 = %+v", w.Asks[0])
	}

	// Same-agent re-ask dedupes without folding anything (§3.7.7).
	out = f.apply(30*time.Second, event.PermissionRequested, "a1", withTool("Bash", ""), withAsk("command", rebuildCmd))
	if hasKind(out, event.GhostAgentFolded) {
		t.Fatal("same-agent re-ask folded a ghost")
	}
	if !hasKind(out, event.AskDeduped) {
		t.Fatal("same-agent re-ask not deduped")
	}
	w = f.m.Snapshot()
	if w.Asks[0].DupeCount != 4 || w.Asks[0].FoldedGhosts != 2 {
		t.Fatalf("ask after re-ask = %+v", w.Asks[0])
	}
	if agentByID(w, "a1") == nil {
		t.Fatal("re-ask folded the original asker")
	}

	// A ghost's later lifecycle events never resurrect its row.
	f.apply(40*time.Second, event.AgentReturned, "g1")
	if agentByID(f.m.Snapshot(), "g1") != nil {
		t.Fatal("returned ghost reappeared")
	}
}

// TestGhostNeverCreated: the duplicate ask can arrive before any spawn event
// for the retry agent; its row must never be created afterwards.
func TestGhostNeverCreated(t *testing.T) {
	f := newFix()
	raiseTestAsk(f)
	f.apply(5*time.Second, event.PermissionRequested, "g9", withTool("Bash", ""), withAsk("command", rebuildCmd))
	f.apply(6*time.Second, event.AgentSpawned, "g9", withName("retry ghost"))
	f.apply(7*time.Second, event.AgentStarted, "g9")
	w := f.m.Snapshot()
	if agentByID(w, "g9") != nil {
		t.Fatal("late spawn created a row for a folded ghost")
	}
	if w.Asks[0].FoldedGhosts != 1 {
		t.Fatalf("FoldedGhosts = %d, want 1", w.Asks[0].FoldedGhosts)
	}
}

// TestAskResolution covers §3.7.8: resolution is asymmetric and never claims
// an outcome that wasn't observed.
func TestAskResolution(t *testing.T) {
	t.Run("granted then next event clears", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.PermissionGranted, "a1")
		w := f.m.Snapshot()
		if len(w.Asks) != 1 || !w.Asks[0].Resolving {
			t.Fatalf("asks after grant = %+v, want one resolving entry", w.Asks)
		}
		if got := agentByID(w, "a1").Status; got != StatusRun {
			t.Fatalf("status = %v, want run", got)
		}
		f.apply(11*time.Second, event.ToolEnd, "a1", withTool("Bash", rebuildCmd))
		if w = f.m.Snapshot(); len(w.Asks) != 0 {
			t.Fatalf("ask not cleared by the next event: %+v", w.Asks)
		}
	})
	t.Run("denied clears immediately", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.PermissionDenied, "a1")
		w := f.m.Snapshot()
		if len(w.Asks) != 0 {
			t.Fatalf("asks after deny = %+v", w.Asks)
		}
		if got := agentByID(w, "a1").Status; got != StatusRun {
			t.Fatalf("status = %v, want run", got)
		}
	})
	t.Run("activity implies the grant", func(t *testing.T) {
		// Granting emits no dedicated hook event; the tool's
		// PostToolUse is the signal (DATA-SOURCES §3.3).
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.ToolEnd, "a1", withTool("Bash", rebuildCmd))
		w := f.m.Snapshot()
		if len(w.Asks) != 1 || !w.Asks[0].Resolving {
			t.Fatalf("asks = %+v, want one resolving entry", w.Asks)
		}
		if got := agentByID(w, "a1").Status; got != StatusRun {
			t.Fatalf("status = %v, want run", got)
		}
		f.apply(11*time.Second, event.TokensUpdated, "a1", withTokens(100, false))
		if w = f.m.Snapshot(); len(w.Asks) != 0 {
			t.Fatalf("ask survived a second event: %+v", w.Asks)
		}
	})
	t.Run("grace period clears a silent resolution", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.PermissionGranted, "a1")
		f.tick(14 * time.Second)
		if w := f.m.Snapshot(); len(w.Asks) != 1 {
			t.Fatalf("ask cleared before the grace expired: %+v", w.Asks)
		}
		f.tick(15 * time.Second)
		if w := f.m.Snapshot(); len(w.Asks) != 0 {
			t.Fatalf("ask survived the grace period: %+v", w.Asks)
		}
	})
	t.Run("unanswered ask persists", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.tick(300 * time.Second)
		w := f.m.Snapshot()
		if len(w.Asks) != 1 || w.Asks[0].Resolving {
			t.Fatalf("unanswered ask = %+v, want one non-resolving entry", w.Asks)
		}
		if got := agentByID(w, "a1").Status; got != StatusAsk {
			t.Fatalf("status = %v, want ask (asks never stall)", got)
		}
	})
	t.Run("return clears the ask", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.AgentReturned, "a1")
		w := f.m.Snapshot()
		if len(w.Asks) != 0 {
			t.Fatalf("asks after return = %+v", w.Asks)
		}
		if got := agentByID(w, "a1").Status; got != StatusDone {
			t.Fatalf("status = %v, want done", got)
		}
	})
	t.Run("re-ask while resolving revives the entry", func(t *testing.T) {
		f := newFix()
		raiseTestAsk(f)
		f.apply(10*time.Second, event.PermissionGranted, "a1")
		f.apply(11*time.Second, event.PermissionRequested, "a1", withTool("Bash", ""), withAsk("command", rebuildCmd))
		w := f.m.Snapshot()
		if len(w.Asks) != 1 || w.Asks[0].Resolving {
			t.Fatalf("asks = %+v, want one live (non-resolving) entry", w.Asks)
		}
	})
}

func TestMainAsk(t *testing.T) {
	f := newFix()
	f.apply(0, event.PermissionRequested, event.MainAgentID,
		withTool("Bash", ""), withAsk("command", "touch /private/tmp/probe"))
	w := f.m.Snapshot()
	if len(w.Asks) != 1 || w.Asks[0].AgentID != event.MainAgentID {
		t.Fatalf("main ask = %+v", w.Asks)
	}
	f.apply(1*time.Second, event.ToolEnd, event.MainAgentID, withTool("Bash", "touch /private/tmp/probe"))
	f.apply(2*time.Second, event.TokensUpdated, event.MainAgentID, withTokens(10, false))
	if w = f.m.Snapshot(); len(w.Asks) != 0 {
		t.Fatalf("main ask not resolved: %+v", w.Asks)
	}
}

func TestTeammateNeverInBand(t *testing.T) {
	f := newFix()
	f.apply(0, event.AgentSpawned, "rev", withType("teammate"), withName("reviewer"))
	f.apply(1*time.Second, event.PermissionRequested, "rev", withTool("Bash", ""), withAsk("command", "rm x"))
	w := f.m.Snapshot()
	if len(w.Asks) != 0 {
		t.Fatalf("teammate raised a band entry: %+v", w.Asks)
	}
	a := agentByID(w, "rev")
	if a == nil || a.Status != StatusTeammate || !a.Teammate {
		t.Fatalf("teammate row = %+v", a)
	}
}

// TestContention covers §2.7: raise on the second live editor, clear when
// one finishes.
func TestContention(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.spawnRun(0, "a2")
	f.apply(1*time.Second, event.FileEdited, "a1", withTool("Edit", "src/db/schema.ts"), withLines(3, 1))
	out := f.apply(2*time.Second, event.FileEdited, "a2", withTool("Edit", "src/db/schema.ts"), withLines(2, 0))
	if !hasKind(out, event.ContentionDetected) {
		t.Fatalf("no ContentionDetected: %v", out)
	}
	w := f.m.Snapshot()
	if len(w.Contentions) != 1 || w.Contentions[0].Path != "src/db/schema.ts" {
		t.Fatalf("contentions = %+v", w.Contentions)
	}
	if len(w.Contentions[0].AgentIDs) != 2 {
		t.Fatalf("editors = %v", w.Contentions[0].AgentIDs)
	}

	// Repeat edits of the same path never re-raise.
	out = f.apply(3*time.Second, event.FileEdited, "a1", withTool("Edit", "src/db/schema.ts"), withLines(1, 1))
	if hasKind(out, event.ContentionDetected) {
		t.Fatal("re-edit re-raised the contention")
	}

	out = f.apply(4*time.Second, event.AgentReturned, "a1")
	if !hasKind(out, event.ContentionCleared) {
		t.Fatalf("no ContentionCleared when an editor finished: %v", out)
	}
	if w = f.m.Snapshot(); len(w.Contentions) != 0 {
		t.Fatalf("contention survived: %+v", w.Contentions)
	}
}

func TestContentionIncludesMain(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(1*time.Second, event.FileEdited, event.MainAgentID, withTool("Edit", "db/schema.ts"), withLines(1, 0))
	out := f.apply(2*time.Second, event.FileEdited, "a1", withTool("Edit", "db/schema.ts"), withLines(1, 0))
	if !hasKind(out, event.ContentionDetected) {
		t.Fatalf("main + subagent on one file did not contend: %v", out)
	}
	w := f.m.Snapshot()
	if len(w.Contentions) != 1 {
		t.Fatalf("contentions = %+v", w.Contentions)
	}
	// A new run resets main's edit set and clears the row.
	out = f.apply(3*time.Second, event.UserPromptSubmitted, "", withDetail("next task"))
	if !hasKind(out, event.ContentionCleared) {
		t.Fatalf("new run did not clear main's stale contention: %v", out)
	}
}

// TestRunLifecycle covers §2.8 in both settle orders.
func TestRunLifecycle(t *testing.T) {
	t.Run("idle then settle", func(t *testing.T) {
		f := newFix()
		f.apply(0, event.SessionStart, "", func(e *event.Event) { e.SessionID = "3f9c-session" })
		out := f.apply(0, event.UserPromptSubmitted, "", withDetail("refactor auth"))
		if !hasKind(out, event.RunStarted) {
			t.Fatalf("no RunStarted: %v", out)
		}
		f.spawnRun(1*time.Second, "a1")
		f.spawnRun(2*time.Second, "a2")
		f.apply(2500*time.Millisecond, event.TokensUpdated, "a1", withTokens(1000, false))

		out = f.apply(3*time.Second, event.SessionIdle, "")
		if hasKind(out, event.RunEnded) {
			t.Fatal("run ended with two live agents")
		}
		out = f.apply(4*time.Second, event.AgentReturned, "a1")
		if hasKind(out, event.RunEnded) {
			t.Fatal("run ended with one live agent")
		}
		out = f.apply(10*time.Second, event.AgentReturned, "a2")
		if !hasKind(out, event.RunEnded) {
			t.Fatalf("run did not end when the last agent settled: %v", out)
		}

		w := f.m.Snapshot()
		if w.Run != nil {
			t.Fatalf("active run survived: %+v", w.Run)
		}
		if w.LastRun == nil {
			t.Fatal("LastRun not set")
		}
		if len(w.LastRun.Agents) != 2 || w.LastRun.Tokens != 1000 || w.LastRun.Prompt != "refactor auth" {
			t.Fatalf("LastRun = %+v", w.LastRun)
		}
		if w.LastRun.Agents[0].Duration != 3*time.Second {
			t.Fatalf("a1 duration = %v, want 3s", w.LastRun.Agents[0].Duration)
		}
		if w.LastRun.Agents[1].Duration != 8*time.Second {
			t.Fatalf("a2 duration = %v, want 8s", w.LastRun.Agents[1].Duration)
		}

		// The RunEnded Detail carries the whole run as JSON.
		var ended *event.Event
		for i := range out {
			if out[i].Kind == event.RunEnded {
				ended = &out[i]
			}
		}
		var got Run
		if err := json.Unmarshal([]byte(ended.Detail), &got); err != nil {
			t.Fatalf("RunEnded detail is not JSON: %v", err)
		}
		if got.ID != w.LastRun.ID || len(got.Agents) != 2 || got.Agents[0].Status != StatusDone {
			t.Fatalf("round-tripped run = %+v", got)
		}
	})
	t.Run("settle then idle", func(t *testing.T) {
		f := newFix()
		f.apply(0, event.UserPromptSubmitted, "", withDetail("go"))
		f.spawnRun(1*time.Second, "a1")
		f.apply(2*time.Second, event.AgentReturned, "a1")
		out := f.apply(3*time.Second, event.SessionIdle, "")
		if !hasKind(out, event.RunEnded) {
			t.Fatalf("run did not end on SessionIdle: %v", out)
		}
	})
	t.Run("session end forces the run closed", func(t *testing.T) {
		f := newFix()
		f.apply(0, event.UserPromptSubmitted, "", withDetail("go"))
		f.spawnRun(1*time.Second, "a1")
		out := f.apply(2*time.Second, event.SessionEnd, "")
		if !hasKind(out, event.RunEnded) {
			t.Fatalf("SessionEnd did not close the run: %v", out)
		}
		if w := f.m.Snapshot(); w.LastRun == nil || len(w.LastRun.Agents) != 1 || w.LastRun.Agents[0].Status != StatusRun {
			t.Fatalf("forced-closed run = %+v", w.LastRun)
		}
	})
	t.Run("stalls and contentions are counted", func(t *testing.T) {
		f := newFix()
		f.apply(0, event.UserPromptSubmitted, "", withDetail("go"))
		f.spawnRun(1*time.Second, "a1")
		f.spawnRun(1*time.Second, "a2")
		f.apply(2*time.Second, event.FileEdited, "a1", withTool("Edit", "x.go"), withLines(1, 0))
		f.apply(3*time.Second, event.FileEdited, "a2", withTool("Edit", "x.go"), withLines(1, 0))
		f.tick(50 * time.Second) // both stall
		w := f.m.Snapshot()
		if w.Run == nil || w.Run.Stalls != 2 || w.Run.Contentions != 1 {
			t.Fatalf("active run = %+v, want 2 stalls, 1 contention", w.Run)
		}
		if !w.Run.EndedAt.IsZero() {
			t.Fatalf("active run has an EndedAt: %v", w.Run.EndedAt)
		}
	})
	t.Run("folded ghosts never block or join the run", func(t *testing.T) {
		f := newFix()
		f.apply(0, event.UserPromptSubmitted, "", withDetail("go"))
		raiseTestAsk(f)
		f.apply(3*time.Second, event.PermissionRequested, "g1", withTool("Bash", ""), withAsk("command", rebuildCmd))
		f.apply(4*time.Second, event.PermissionDenied, "a1")
		f.apply(5*time.Second, event.AgentReturned, "a1")
		out := f.apply(6*time.Second, event.SessionIdle, "")
		if !hasKind(out, event.RunEnded) {
			t.Fatalf("folded ghost blocked the run end: %v", out)
		}
		w := f.m.Snapshot()
		if len(w.LastRun.Agents) != 1 || w.LastRun.Agents[0].ID != "a1" {
			t.Fatalf("run agents = %+v, want a1 only", w.LastRun.Agents)
		}
	})
}

// TestDecay covers §3.5: done + 30s → one AgentDecayed, flagged in the
// snapshot, never repeated.
func TestDecay(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(10*time.Second, event.AgentReturned, "a1")
	out := f.tick(39 * time.Second)
	if hasKind(out, event.AgentDecayed) {
		t.Fatal("decayed at 29s")
	}
	out = f.tick(40 * time.Second)
	if !hasKind(out, event.AgentDecayed) {
		t.Fatalf("no AgentDecayed at 30s: %v", out)
	}
	a := agentByID(f.m.Snapshot(), "a1")
	if !a.Decayed || a.Status != StatusDone {
		t.Fatalf("agent = decayed=%v status=%v", a.Decayed, a.Status)
	}
	if out = f.tick(41 * time.Second); hasKind(out, event.AgentDecayed) {
		t.Fatal("AgentDecayed repeated")
	}
}

// TestOrdering covers §3.6: ask → stuck → run → queued → done → teammate →
// unknown, stable by spawn order, and only attention may reorder.
func TestOrdering(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1") // run
	f.spawnRun(0, "a2") // done below
	f.apply(1*time.Second, event.AgentReturned, "a2")
	f.spawnRun(0, "a3")                                        // run
	f.apply(0, event.AgentSpawned, "a4")                       // queued
	f.apply(0, event.AgentSpawned, "a5", withType("teammate")) // teammate
	f.spawnRun(0, "a6")                                        // ask below
	f.apply(2*time.Second, event.PermissionRequested, "a6", withTool("Bash", ""), withAsk("command", "rm x"))

	order := func() []string {
		w := f.m.Snapshot()
		ids := make([]string, len(w.Agents))
		for i, a := range w.Agents {
			ids[i] = a.ID
		}
		return ids
	}
	want := []string{"a6", "a1", "a3", "a4", "a2", "a5"}
	got := order()
	if !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	// A token update is not attention: nothing moves.
	f.apply(3*time.Second, event.TokensUpdated, "a3", withTokens(9000, false))
	if got = order(); !equalStrings(got, want) {
		t.Fatalf("token update reordered rows: %v", got)
	}

	// A new ask is attention: a1 floats, stable by spawn within the group.
	f.apply(4*time.Second, event.PermissionRequested, "a1", withTool("Bash", ""), withAsk("command", "rm y"))
	want = []string{"a1", "a6", "a3", "a4", "a2", "a5"}
	if got = order(); !equalStrings(got, want) {
		t.Fatalf("order after second ask = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSnapshotIsolation: mutating a returned World must never touch machine
// state.
func TestSnapshotIsolation(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(1*time.Second, event.ToolEnd, "a1", withTool("Bash", "go test ./..."))
	f.apply(2*time.Second, event.FileEdited, "a1", withTool("Edit", "x.go"), withLines(1, 0))
	f.apply(2500*time.Millisecond, event.ToolStart, "a1", withTool("Bash", "rm x")) // leaves a call open
	f.apply(3*time.Second, event.PermissionRequested, "a1", withTool("Bash", ""),
		func(e *event.Event) {
			e.Ask = &event.AskPayload{Kind: "command", Command: "rm x", Options: []string{"once", "no"}}
		})

	w1 := f.m.Snapshot()
	w1.Agents[0].CallHistory[0].Tool = "HACKED"
	w1.Agents[0].EditedFiles[0] = "HACKED"
	w1.Agents[0].Ask.Options[0] = "HACKED"
	w1.Agents[0].OpenCall.Tool = "HACKED"
	w1.Asks[0].Command = "HACKED"

	w2 := f.m.Snapshot()
	a := agentByID(w2, "a1")
	if a.CallHistory[0].Tool == "HACKED" || a.EditedFiles[0] == "HACKED" ||
		a.Ask.Options[0] == "HACKED" || a.OpenCall.Tool == "HACKED" || w2.Asks[0].Command == "HACKED" {
		t.Fatal("snapshot shares mutable state with the machine")
	}
}

// TestAwayMarkData covers the §3.19 contract: per-agent Rev moves against
// World.Rev exactly when a row changes.
func TestAwayMarkData(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.spawnRun(0, "a2")
	before := f.m.Snapshot()
	a1rev := agentByID(before, "a1").Rev
	a2rev := agentByID(before, "a2").Rev

	f.apply(1*time.Second, event.ToolStart, "a2", withTool("Read", "x.go"))
	after := f.m.Snapshot()
	if got := agentByID(after, "a1").Rev; got != a1rev {
		t.Fatalf("untouched agent's Rev moved: %d → %d", a1rev, got)
	}
	if got := agentByID(after, "a2").Rev; got <= a2rev {
		t.Fatalf("touched agent's Rev did not move: %d → %d", a2rev, got)
	}
	if after.Rev <= before.Rev {
		t.Fatalf("World.Rev did not move: %d → %d", before.Rev, after.Rev)
	}
}
