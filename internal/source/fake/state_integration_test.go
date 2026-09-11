package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/slug"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/state"
)

// Replays the demo timeline through the real state machine and asserts the
// §2.10 snapshot row by row. This is the seam PART 10 measures --demo against.
func TestDemoTimelineReproducesSection210(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := fake.New(fake.Options{SeekSeconds: fake.SnapshotSeconds})
	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	m := state.New(state.DefaultConfig())
	var count int
drain:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break drain
			}
			m.Apply(ev, ev.At)
			count++
		case <-time.After(500 * time.Millisecond):
			break drain
		}
	}
	if count < 50 {
		t.Fatalf("suspiciously few events drained: %d", count)
	}

	now := fake.Epoch().Add(fake.SnapshotSeconds * time.Second)
	m.Tick(now)
	w := m.Snapshot()

	names := slug.New(18)
	byslug := map[string]state.Agent{}
	for _, a := range w.Agents {
		s := names.Assign(a.ID, a.Name)
		if a.Teammate && a.Name != "" {
			s = a.Name
		}
		byslug[s] = a
	}

	// §2.10 prints this row as "typecheck-worksp…" (16 chars kept), but
	// §3.16's normative truncation example keeps 15 ("document-constr…").
	// The rule definition wins over the illustrative table.
	wantStatus := map[string]state.Status{
		"fix-ts2345-fallout": state.StatusAsk,
		"typecheck-works…":   state.StatusStuck,
		"run-auth-tests":     state.StatusRun,
		"audit-users-schema": state.StatusRun,
		"write-email-index":  state.StatusRun,
		"lint-and-format":    state.StatusDone,
		"document-constr…":   state.StatusQueued,
		"reviewer":           state.StatusTeammate,
	}
	if len(w.Agents) != len(wantStatus) {
		got := make([]string, 0, len(w.Agents))
		for s := range byslug {
			got = append(got, s)
		}
		t.Fatalf("agent count = %d, want %d (ghosts must fold); got %v", len(w.Agents), len(wantStatus), got)
	}
	for s, want := range wantStatus {
		a, ok := byslug[s]
		if !ok {
			t.Errorf("missing §2.10 agent %q", s)
			continue
		}
		if a.Status != want {
			t.Errorf("%s status = %v, want %v", s, a.Status, want)
		}
	}

	fix := byslug["fix-ts2345-fallout"]
	if fix.Retries != 2 {
		t.Errorf("fix retries = %d, want 2 (↺2)", fix.Retries)
	}
	if len(w.Asks) != 1 {
		t.Fatalf("asks = %d, want 1 deduped band entry", len(w.Asks))
	}
	ask := w.Asks[0]
	if ask.DupeCount != 3 {
		t.Errorf("ask DupeCount = %d, want 3 (×3)", ask.DupeCount)
	}
	if ask.FoldedGhosts != 2 {
		t.Errorf("ask FoldedGhosts = %d, want 2", ask.FoldedGhosts)
	}
	if got := now.Sub(ask.RaisedAt); got != 52*time.Second {
		t.Errorf("ask age = %v, want 52s", got)
	}
	if ask.Command != "rm -rf node_modules/.cache && pnpm rebuild" {
		t.Errorf("ask command = %q", ask.Command)
	}

	tests := byslug["run-auth-tests"]
	if tests.OpenCall == nil {
		t.Fatalf("run-auth-tests must have an open call (◐ in Bash)")
	} else if got := now.Sub(tests.OpenCall.Since); got != 192*time.Second {
		t.Errorf("open-call age = %v, want 3m12s", got)
	}
	if !tests.Verified {
		t.Errorf("run-auth-tests must carry the inferred verified marker")
	}

	if lint := byslug["lint-and-format"]; !lint.Decayed {
		t.Errorf("lint-and-format returned 46s ago and must be decayed")
	}
	if ty := byslug["typecheck-works…"]; ty.OpenCall != nil {
		t.Errorf("typecheck-worksp… must have no open call (true flatline stall)")
	}

	// The §2.7 contention block was removed from the UI, and the demo no longer
	// stages the schema.ts collision that fed it. Detection still works — this
	// asserts the scenario, not the machine.
	if len(w.Contentions) != 0 {
		t.Errorf("contentions = %d, want 0: the demo no longer stages a shared edit", len(w.Contentions))
	}

	if w.Main.Todo != "write the migration" {
		t.Errorf("main todo = %q, want %q", w.Main.Todo, "write the migration")
	}

	// §3.6 order: the ask row leads, teammate is last.
	if first := w.Agents[0]; first.Status != state.StatusAsk {
		t.Errorf("first rendered agent = %v, want the ask", first.Status)
	}
	if last := w.Agents[len(w.Agents)-1]; last.Status != state.StatusTeammate {
		t.Errorf("last rendered agent = %v, want the teammate", last.Status)
	}
	_ = event.MainAgentID
}
