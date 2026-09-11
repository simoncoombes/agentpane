package fake

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

func offset(ev event.Event) time.Duration { return ev.At.Sub(epoch) }

func byAgent(evs []event.Event) map[string][]event.Event {
	m := map[string][]event.Event{}
	for _, ev := range evs {
		m[ev.AgentID] = append(m[ev.AgentID], ev)
	}
	return m
}

func lastOf(evs []event.Event) event.Event { return evs[len(evs)-1] }

// openCall reports whether the agent's final ToolStart has no later ToolEnd.
func openCall(evs []event.Event) (event.Event, bool) {
	var last event.Event
	open := false
	for _, ev := range evs {
		switch ev.Kind {
		case event.ToolStart:
			last, open = ev, true
		case event.ToolEnd:
			open = false
		}
	}
	return last, open
}

func TestScriptInvariants(t *testing.T) {
	evs := script()
	hexID := regexp.MustCompile(`^[0-9a-f]{17}$`)
	prev := epoch
	for i, ev := range evs {
		if ev.Kind.Derived() {
			t.Errorf("event %d: source emitted derived kind %v", i, ev.Kind)
		}
		if ev.Kind == event.KindInvalid {
			t.Errorf("event %d: invalid kind", i)
		}
		if ev.SessionID != sessionID {
			t.Errorf("event %d: SessionID %q", i, ev.SessionID)
		}
		if ev.At.Before(prev) {
			t.Errorf("event %d: timestamps regress (%v after %v)", i, ev.At, prev)
		}
		prev = ev.At
		if ev.AgentID != "" && ev.AgentID != event.MainAgentID && !hexID.MatchString(ev.AgentID) {
			t.Errorf("event %d: agent id %q is not 17-hex", i, ev.AgentID)
		}
		if ev.Ask != nil && ev.Kind != event.PermissionRequested {
			t.Errorf("event %d: Ask set on %v", i, ev.Kind)
		}
	}
	if last := offset(lastOf(evs)); last > SnapshotSeconds*time.Second {
		t.Errorf("script runs past the snapshot: last event at %v", last)
	}
}

// TestCastCoverage verifies every §2.10 row is caused by scripted events.
func TestCastCoverage(t *testing.T) {
	evs := script()
	agents := byAgent(evs)
	snapshot := SnapshotSeconds * time.Second

	t.Run("prompt starts the run at t=0", func(t *testing.T) {
		for _, ev := range evs {
			if ev.Kind == event.UserPromptSubmitted {
				if !ev.At.Equal(epoch) || ev.AgentID != event.MainAgentID || ev.Detail != "refactor auth" {
					t.Fatalf("prompt event wrong: %+v", ev)
				}
				return
			}
		}
		t.Fatal("no UserPromptSubmitted")
	})

	t.Run("main: run, todo write-the-migration, 112k", func(t *testing.T) {
		main := agents[event.MainAgentID]
		var lastTodo, lastTokens event.Event
		for _, ev := range main {
			if ev.Tool == "TodoWrite" {
				lastTodo = ev
			}
			if ev.Kind == event.TokensUpdated {
				lastTokens = ev
			}
		}
		if lastTodo.Detail != "write the migration" {
			t.Errorf("current todo %q, want %q", lastTodo.Detail, "write the migration")
		}
		if !lastTokens.TokensCum || lastTokens.Tokens != 112000 {
			t.Errorf("main tokens %+v, want cumulative 112000", lastTokens)
		}
		if since := snapshot - offset(lastOf(main)); since > 10*time.Second {
			t.Errorf("main silent %v at snapshot; must be live", since)
		}
	})

	t.Run("fix-ts2345-fallout: ask blocked 52s", func(t *testing.T) {
		fix := agents[idFix]
		if fix[0].Kind != event.AgentSpawned || fix[0].AgentName != descFix {
			t.Fatalf("spawn must carry the full description, got %+v", fix[0])
		}
		last := lastOf(fix)
		if last.Kind != event.PermissionRequested {
			t.Fatalf("last fix event is %v, want PermissionRequested (blocked)", last.Kind)
		}
		if got := snapshot - offset(last); got != 52*time.Second {
			t.Errorf("blocked %v at snapshot, want 52s", got)
		}
		if last.Ask == nil || last.Ask.Command != askCommand || last.Ask.Kind != "command" {
			t.Errorf("ask payload wrong: %+v", last.Ask)
		}
	})

	t.Run("fix-ts2345-fallout: two inferred retries and failing typecheck", func(t *testing.T) {
		fix := agents[idFix]
		starts, failEnds := 0, 0
		for i, ev := range fix {
			if ev.Kind == event.ToolStart && ev.Tool == "Bash" && ev.Target == typecheckCmd {
				starts++
				// A retry is the same command re-issued after a failing
				// ToolEnd, so every start after the first must follow one.
				if starts > 1 {
					found := false
					for j := i - 1; j >= 0; j-- {
						if fix[j].Kind == event.ToolEnd && fix[j].Target == typecheckCmd {
							found = fix[j].ExitCode != nil && *fix[j].ExitCode != 0
							break
						}
					}
					if !found {
						t.Errorf("re-issue %d not preceded by a failing ToolEnd", starts)
					}
				}
			}
			if ev.Kind == event.ToolEnd && ev.Target == typecheckCmd {
				if ev.ExitCode == nil || *ev.ExitCode == 0 || ev.Err == "" {
					t.Errorf("typecheck end must fail: %+v", ev)
				}
				if !strings.Contains(ev.Err, "TS2345") || !strings.Contains(ev.Detail, "handlers.ts:88") {
					t.Errorf("failure must cite TS2345 handlers.ts:88, got %q / %q", ev.Err, ev.Detail)
				}
				failEnds++
			}
		}
		if starts != 3 || failEnds != 3 {
			t.Errorf("typecheck issued %d times with %d failures, want 3/3 (two re-issues = dim x2)", starts, failEnds)
		}
	})

	t.Run("fix-ts2345-fallout: call history has the edit", func(t *testing.T) {
		for _, ev := range agents[idFix] {
			if ev.Kind == event.FileEdited && ev.Target == "api/types.ts" {
				if ev.LinesAdd != 29 || ev.LinesDel != 4 {
					t.Errorf("edit %+d -%d, want +29 -4", ev.LinesAdd, ev.LinesDel)
				}
				return
			}
		}
		t.Fatal("no FileEdited api/types.ts")
	})

	t.Run("ask deduped x3 with two foldable ghosts", func(t *testing.T) {
		ids := map[string]bool{}
		for _, ev := range evs {
			if ev.Kind == event.PermissionRequested {
				if ev.Tool != "Bash" || ev.Target != askCommand || ev.Ask == nil || ev.Ask.Command != askCommand {
					t.Errorf("ask not dedupable on tool+input: %+v", ev)
				}
				ids[ev.AgentID] = true
			}
		}
		if len(ids) != 3 || !ids[idFix] || !ids[idGhost1] || !ids[idGhost2] {
			t.Fatalf("asks under %v, want fix + 2 ghosts", ids)
		}
		for _, ghost := range []string{idGhost1, idGhost2} {
			for _, ev := range agents[ghost] {
				if ev.Kind != event.PermissionRequested {
					t.Errorf("ghost %s has non-ask event %v; it must fold cleanly", ghost, ev.Kind)
				}
			}
		}
	})

	t.Run("typecheck-workspace: stuck, silent 2m50s, no call open", func(t *testing.T) {
		typ := agents[idType]
		if typ[0].AgentName != descType {
			t.Errorf("description %q", typ[0].AgentName)
		}
		if _, open := openCall(typ); open {
			t.Error("a call is open; §2.10 requires none")
		}
		if since := snapshot - offset(lastOf(typ)); since != 170*time.Second {
			t.Errorf("silent %v at snapshot, want 2m50s", since)
		}
		edited := false
		for _, ev := range typ {
			edited = edited || ev.Kind == event.FileEdited
		}
		if !edited {
			t.Error("needs a FileEdited so its pips read spawn/tool/edit")
		}
	})

	t.Run("run-auth-tests: open watch call 3m12s, verified, alive", func(t *testing.T) {
		tests := agents[idTests]
		call, open := openCall(tests)
		if !open || call.Tool != "Bash" || call.Target != watchCmd {
			t.Fatalf("open call = %+v (open=%v), want Bash %q", call, open, watchCmd)
		}
		if inCall := snapshot - offset(call); inCall != 192*time.Second {
			t.Errorf("in call %v at snapshot, want 3m12s", inCall)
		}
		verified, lastOut := false, time.Duration(-1)
		for _, ev := range tests {
			if ev.Kind == event.ToolEnd && strings.Contains(ev.Target, "test") && ev.ExitCode != nil && *ev.ExitCode == 0 {
				verified = true // the pattern VerifyObserved infers from
			}
			if ev.Kind == event.ToolOutput {
				if offset(ev) <= offset(call) {
					t.Errorf("ToolOutput before the background call opened")
				}
				lastOut = offset(ev)
			}
		}
		if !verified {
			t.Error("no completed passing test command for the verified marker")
		}
		if lastOut < 0 || snapshot-lastOut > 10*time.Second {
			t.Errorf("background output stale (%v); the watch must read alive", lastOut)
		}
	})

	t.Run("audit-users-schema: grepping createUser, edited schema.ts", func(t *testing.T) {
		audit := agents[idAudit]
		last := lastOf(audit)
		if last.Kind != event.ToolEnd || last.Tool != "Grep" || !strings.Contains(last.Target, "createUser") {
			t.Errorf("latest activity %+v, want a closed createUser Grep", last)
		}
		if since := snapshot - offset(last); since > 5*time.Second {
			t.Errorf("audit silent %v; must be live", since)
		}
		edited := false
		for _, ev := range audit {
			edited = edited || (ev.Kind == event.FileEdited && ev.Target == "src/db/schema.ts")
		}
		if !edited {
			t.Error("must edit src/db/schema.ts — it is the schema this agent audits")
		}
	})

	t.Run("write-email-index: 0042.sql +84 -3, and no shared-file edit", func(t *testing.T) {
		index := agents[idIndex]
		var lastEdit event.Event
		schema := false
		for _, ev := range index {
			if ev.Kind == event.FileEdited {
				lastEdit = ev
				schema = schema || ev.Target == "src/db/schema.ts"
			}
		}
		if !strings.HasSuffix(lastEdit.Target, "0042.sql") || lastEdit.LinesAdd != 84 || lastEdit.LinesDel != 3 {
			t.Errorf("latest edit %+v, want 0042.sql +84 -3", lastEdit)
		}
		// This agent used to edit src/db/schema.ts at the same second as
		// audit-users-schema, purely to drive the §2.7 contention block. That block
		// is gone, so the shared edit drove nothing — and a demo that stages a
		// collision it cannot show is a demo that lies about the tool.
		if schema {
			t.Error("write-email-index must not edit src/db/schema.ts — the contention it staged renders nowhere")
		}
		// Both stay live to the snapshot: the tree shows them as RUN agents.
		for _, id := range []string{idAudit, idIndex} {
			for _, ev := range agents[id] {
				if ev.Kind == event.AgentReturned {
					t.Errorf("%s returned; the demo shows it running at the snapshot", id)
				}
			}
		}
	})

	t.Run("lint-and-format: returned 46s ago, decayed", func(t *testing.T) {
		lint := agents[idLint]
		last := lastOf(lint)
		if last.Kind != event.AgentReturned {
			t.Fatalf("last lint event %v, want AgentReturned", last.Kind)
		}
		ago := snapshot - offset(last)
		if ago != 46*time.Second {
			t.Errorf("returned %v before snapshot, want 46s", ago)
		}
		if ago <= 30*time.Second {
			t.Error("must be older than the 30s decay threshold")
		}
	})

	t.Run("document-constraint: queued, spawn only", func(t *testing.T) {
		docs := agents[idDocs]
		if len(docs) != 1 || docs[0].Kind != event.AgentSpawned {
			t.Fatalf("queued agent has %d events, want a single AgentSpawned", len(docs))
		}
		if docs[0].AgentName != descDocs {
			t.Errorf("description %q", docs[0].AgentName)
		}
	})

	t.Run("reviewer: teammate, near-zero telemetry", func(t *testing.T) {
		rev := agents[idReviewer]
		if len(rev) != 1 || rev[0].Kind != event.AgentSpawned {
			t.Fatalf("teammate has %d events, want a single AgentSpawned", len(rev))
		}
		if rev[0].AgentType != "teammate" || rev[0].AgentName != "reviewer" {
			t.Errorf("teammate spawn wrong: %+v", rev[0])
		}
	})

	t.Run("token deltas feed every live sparkline", func(t *testing.T) {
		finals := map[string]int{}
		for _, id := range []string{event.MainAgentID, idFix, idType, idTests, idAudit, idIndex, idLint} {
			n, prevCum := 0, 0
			for _, ev := range agents[id] {
				if ev.Kind != event.TokensUpdated {
					continue
				}
				n++
				if !ev.TokensCum {
					t.Errorf("%s: TokensUpdated not cumulative", id)
				}
				if ev.Tokens <= prevCum {
					t.Errorf("%s: cumulative tokens not increasing (%d -> %d)", id, prevCum, ev.Tokens)
				}
				prevCum = ev.Tokens
			}
			if n < 3 {
				t.Errorf("%s: only %d token updates; sparkline needs deltas", id, n)
			}
			finals[id] = prevCum
		}
		total := 0
		for _, v := range finals {
			total += v
		}
		if total != 412000 {
			t.Errorf("run token total %d, want 412000 (§3.18 header: 412k tok)", total)
		}
	})

	t.Run("no run-status agent is accidentally quiet past 45s", func(t *testing.T) {
		// Gaps with no open call and no events past 45s read as stuck
		// (§2.5). Only typecheck-workspace may ever hit that state.
		for _, id := range []string{event.MainAgentID, idFix, idAudit, idIndex, idLint} {
			evs := agents[id]
			prev := offset(evs[0])
			for _, ev := range evs[1:] {
				if gap := offset(ev) - prev; gap > 45*time.Second {
					t.Errorf("%s: %v gap before %v at %v", id, gap, ev.Kind, offset(ev))
				}
				prev = offset(ev)
			}
		}
	})
}
