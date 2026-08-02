package state

import (
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

var t0 = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

type fixture struct {
	m   *Machine
	seq uint64
}

func newFix(cfg ...Config) *fixture {
	c := DefaultConfig()
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return &fixture{m: New(c)}
}

func (f *fixture) at(offset time.Duration) time.Time { return t0.Add(offset) }

func (f *fixture) apply(offset time.Duration, kind event.Kind, agent string, muts ...func(*event.Event)) []event.Event {
	f.seq++
	ev := event.Event{Seq: f.seq, At: f.at(offset), AgentID: agent, Kind: kind}
	for _, mu := range muts {
		mu(&ev)
	}
	return f.m.Apply(ev, f.at(offset))
}

func (f *fixture) tick(offset time.Duration) []event.Event { return f.m.Tick(f.at(offset)) }

func (f *fixture) spawnRun(offset time.Duration, id string) {
	f.apply(offset, event.AgentSpawned, id, withName("agent "+id))
	f.apply(offset, event.AgentStarted, id)
}

func withTool(tool, target string) func(*event.Event) {
	return func(e *event.Event) { e.Tool = tool; e.Target = target }
}

func withDetail(d string) func(*event.Event) {
	return func(e *event.Event) { e.Detail = d }
}

func withName(n string) func(*event.Event) {
	return func(e *event.Event) { e.AgentName = n }
}

func withType(t string) func(*event.Event) {
	return func(e *event.Event) { e.AgentType = t }
}

func withAsk(kind, cmd string) func(*event.Event) {
	return func(e *event.Event) { e.Ask = &event.AskPayload{Kind: kind, Command: cmd} }
}

func withTokens(n int, cum bool) func(*event.Event) {
	return func(e *event.Event) { e.Tokens = n; e.TokensCum = cum }
}

func withLines(add, del int) func(*event.Event) {
	return func(e *event.Event) { e.LinesAdd = add; e.LinesDel = del }
}

func withErr(s string) func(*event.Event) {
	return func(e *event.Event) { e.Err = s }
}

func withExit(code int) func(*event.Event) {
	return func(e *event.Event) { e.ExitCode = &code }
}

func agentByID(w World, id string) *Agent {
	for i := range w.Agents {
		if w.Agents[i].ID == id {
			return &w.Agents[i]
		}
	}
	return nil
}

// quietByID finds an agent the snapshot suppressed from the tree (§1.5). It
// is still in the model; only its row was not earned.
func quietByID(w World, id string) *QuietAgent {
	for i := range w.Quiet {
		if w.Quiet[i].ID == id {
			return &w.Quiet[i]
		}
	}
	return nil
}

func hasKind(evs []event.Event, k event.Kind) bool {
	for _, e := range evs {
		if e.Kind == k {
			return true
		}
	}
	return false
}

// TestTransitions covers every §2.4 transition.
func TestTransitions(t *testing.T) {
	type step struct {
		dt    time.Duration
		tick  bool
		kind  event.Kind
		agent string
		muts  []func(*event.Event)
	}
	cases := []struct {
		name        string
		steps       []step
		agent       string
		want        Status
		wantDerived []event.Kind
	}{
		{
			name:  "spawned to queued",
			steps: []step{{kind: event.AgentSpawned, agent: "a1"}},
			agent: "a1", want: StatusQueued,
		},
		{
			name: "queued to run via AgentStarted",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{dt: time.Second, kind: event.AgentStarted, agent: "a1"},
			},
			agent: "a1", want: StatusRun,
		},
		{
			name: "queued to run via ToolStart",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{dt: time.Second, kind: event.ToolStart, agent: "a1", muts: []func(*event.Event){withTool("Grep", "createUser")}},
			},
			agent: "a1", want: StatusRun,
		},
		{
			name: "run to ask via PermissionRequested",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.PermissionRequested, agent: "a1", muts: []func(*event.Event){withTool("Bash", ""), withAsk("command", "rm -rf x")}},
			},
			agent: "a1", want: StatusAsk,
		},
		{
			name: "ask to run via PermissionGranted",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.PermissionRequested, agent: "a1", muts: []func(*event.Event){withTool("Bash", ""), withAsk("command", "rm -rf x")}},
				{dt: 2 * time.Second, kind: event.PermissionGranted, agent: "a1"},
			},
			agent: "a1", want: StatusRun,
		},
		{
			name: "ask to run via PermissionDenied",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.PermissionRequested, agent: "a1", muts: []func(*event.Event){withTool("Bash", ""), withAsk("command", "rm -rf x")}},
				{dt: 2 * time.Second, kind: event.PermissionDenied, agent: "a1"},
			},
			agent: "a1", want: StatusRun,
		},
		{
			name: "run to stuck via silence",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: 46 * time.Second, tick: true},
			},
			agent: "a1", want: StatusStuck,
			wantDerived: []event.Kind{event.StallDetected},
		},
		{
			name: "stuck cleared by any agent event",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: 46 * time.Second, tick: true},
				{dt: 50 * time.Second, kind: event.TokensUpdated, agent: "a1", muts: []func(*event.Event){withTokens(10, false)}},
			},
			agent: "a1", want: StatusRun,
			wantDerived: []event.Kind{event.StallDetected, event.StallCleared},
		},
		{
			name: "run to done",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.AgentReturned, agent: "a1", muts: []func(*event.Event){withDetail("done")}},
			},
			agent: "a1", want: StatusDone,
		},
		{
			name: "stuck to done",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: 46 * time.Second, tick: true},
				{dt: 50 * time.Second, kind: event.AgentReturned, agent: "a1"},
			},
			agent: "a1", want: StatusDone,
		},
		{
			name: "ask to done",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.PermissionRequested, agent: "a1", muts: []func(*event.Event){withTool("Bash", ""), withAsk("command", "rm -rf x")}},
				{dt: 2 * time.Second, kind: event.AgentReturned, agent: "a1"},
			},
			agent: "a1", want: StatusDone,
		},
		{
			name: "run to unknown after 10m silence",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: 601 * time.Second, tick: true},
			},
			agent: "a1", want: StatusUnknown,
		},
		{
			name: "queued to unknown after 10m silence",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{dt: 601 * time.Second, tick: true},
			},
			agent: "a1", want: StatusUnknown,
		},
		{
			name: "teammate is terminal",
			steps: []step{
				{kind: event.AgentSpawned, agent: "rev", muts: []func(*event.Event){withType("teammate"), withName("reviewer")}},
				{dt: time.Second, kind: event.AgentReturned, agent: "rev"},
				{dt: 601 * time.Second, tick: true},
			},
			agent: "rev", want: StatusTeammate,
		},
		{
			name: "done ignores late activity",
			steps: []step{
				{kind: event.AgentSpawned, agent: "a1"},
				{kind: event.AgentStarted, agent: "a1"},
				{dt: time.Second, kind: event.AgentReturned, agent: "a1"},
				{dt: 2 * time.Second, kind: event.FileEdited, agent: "a1", muts: []func(*event.Event){withTool("Edit", "x.go"), withLines(1, 0)}},
			},
			agent: "a1", want: StatusDone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFix()
			var derived []event.Event
			for _, s := range tc.steps {
				if s.tick {
					derived = append(derived, f.tick(s.dt)...)
					continue
				}
				derived = append(derived, f.apply(s.dt, s.kind, s.agent, s.muts...)...)
			}
			a := agentByID(f.m.Snapshot(), tc.agent)
			if a == nil {
				t.Fatalf("agent %q missing from snapshot", tc.agent)
			}
			if a.Status != tc.want {
				t.Fatalf("status = %v, want %v", a.Status, tc.want)
			}
			for _, k := range tc.wantDerived {
				if !hasKind(derived, k) {
					t.Errorf("derived events missing %v (got %v)", k, derived)
				}
			}
		})
	}
}

// TestStallThresholds covers §2.5, including a quiet --watch beating the
// allowlist and an allowlisted build not stalling at 46s.
func TestStallThresholds(t *testing.T) {
	cases := []struct {
		name        string
		tool        string // open-call tool; "" = no open call
		cmd         string
		watchExempt bool // value for WatchNeverExempt
		quiet       time.Duration
		want        Status
	}{
		{"no call 44s is fine", "", "", true, 44 * time.Second, StatusRun},
		{"no call 45s is stuck", "", "", true, 45 * time.Second, StatusStuck},
		{"allowlisted build not stuck at 46s", "Bash", "pnpm build", true, 46 * time.Second, StatusRun},
		{"allowlisted build stuck at 181s", "Bash", "pnpm build", true, 181 * time.Second, StatusStuck},
		{"quiet watch beats the allowlist", "Bash", "pnpm test --watch", true, 46 * time.Second, StatusStuck},
		{"watch exemption off defers to allowlist", "Bash", "pnpm test --watch", false, 46 * time.Second, StatusRun},
		{"watch exemption off still stalls eventually", "Bash", "pnpm test --watch", false, 181 * time.Second, StatusStuck},
		{"-w flag beats the allowlist", "Bash", "npm run test -w", true, 46 * time.Second, StatusStuck},
		{"tail -f beats an allowlisted-looking path", "Bash", "tail -f build.log", true, 46 * time.Second, StatusStuck},
		{"plain command stuck at 46s", "Bash", "sleep 600", true, 46 * time.Second, StatusStuck},
		{"non-Bash open call uses the default", "Agent", "explore the schema", true, 46 * time.Second, StatusStuck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.WatchNeverExempt = tc.watchExempt
			f := newFix(cfg)
			f.spawnRun(0, "a1")
			if tc.tool != "" {
				f.apply(0, event.ToolStart, "a1", withTool(tc.tool, tc.cmd))
			}
			f.tick(tc.quiet)
			a := agentByID(f.m.Snapshot(), "a1")
			if a.Status != tc.want {
				t.Fatalf("after %s quiet: status = %v, want %v", tc.quiet, a.Status, tc.want)
			}
		})
	}
}

func TestThinkingNeverExtendsThreshold(t *testing.T) {
	// ThinkingEnded is a real event (refreshes the clock) but must never
	// change which threshold applies: an open plain call stays on 45s.
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(0, event.ToolStart, "a1", withTool("Bash", "sleep 600"))
	f.apply(10*time.Second, event.ThinkingEnded, "a1", withDetail("planning"))
	f.tick(54 * time.Second) // 44s after the thinking event
	if got := agentByID(f.m.Snapshot(), "a1").Status; got != StatusRun {
		t.Fatalf("status = %v, want run (44s quiet)", got)
	}
	f.tick(56 * time.Second) // 46s after
	if got := agentByID(f.m.Snapshot(), "a1").Status; got != StatusStuck {
		t.Fatalf("status = %v, want stuck (46s quiet, threshold still 45s)", got)
	}
}

// TestStationMonotonic covers §2.2: stations never regress, even when events
// arrive out of order, and advance regardless of status.
func TestStationMonotonic(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	if got := agentByID(f.m.Snapshot(), "a1").Station; got != 0 {
		t.Fatalf("station after spawn = %d, want 0", got)
	}
	// Out of order: the edit lands before its ToolStart.
	f.apply(2*time.Second, event.FileEdited, "a1", withTool("Edit", "x.go"), withLines(4, 1))
	if got := agentByID(f.m.Snapshot(), "a1").Station; got != 2 {
		t.Fatalf("station after edit = %d, want 2", got)
	}
	f.apply(1*time.Second, event.ToolStart, "a1", withTool("Edit", "x.go"))
	if got := agentByID(f.m.Snapshot(), "a1").Station; got != 2 {
		t.Fatalf("station regressed to %d after late ToolStart, want 2", got)
	}
	f.apply(3*time.Second, event.AgentReturned, "a1")
	if got := agentByID(f.m.Snapshot(), "a1").Station; got != 3 {
		t.Fatalf("station after return = %d, want 3", got)
	}
	f.apply(4*time.Second, event.FileEdited, "a1", withTool("Edit", "y.go"), withLines(1, 0))
	a := agentByID(f.m.Snapshot(), "a1")
	if a.Station != 3 || a.Status != StatusDone {
		t.Fatalf("late edit changed station/status: %d/%v, want 3/done", a.Station, a.Status)
	}
}

// TestDuplicateEventIdempotent covers §1.4: the same (AgentID, Kind, Seq)
// applied twice changes nothing.
func TestDuplicateEventIdempotent(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")

	edit := event.Event{Seq: 100, At: f.at(time.Second), AgentID: "a1", Kind: event.FileEdited,
		Tool: "Edit", Target: "x.go", LinesAdd: 10, LinesDel: 2}
	f.m.Apply(edit, f.at(time.Second))
	f.m.Apply(edit, f.at(time.Second))
	a := agentByID(f.m.Snapshot(), "a1")
	if a.LinesAdd != 10 || a.LinesDel != 2 {
		t.Fatalf("duplicate edit double-counted: +%d −%d, want +10 −2", a.LinesAdd, a.LinesDel)
	}

	tok := event.Event{Seq: 101, At: f.at(2 * time.Second), AgentID: "a1", Kind: event.TokensUpdated, Tokens: 500}
	f.m.Apply(tok, f.at(2*time.Second))
	f.m.Apply(tok, f.at(2*time.Second))
	if got := agentByID(f.m.Snapshot(), "a1").Tokens; got != 500 {
		t.Fatalf("duplicate token delta double-counted: %d, want 500", got)
	}

	// A distinct event of the same shape still counts.
	tok2 := tok
	tok2.Seq = 102
	f.m.Apply(tok2, f.at(3*time.Second))
	if got := agentByID(f.m.Snapshot(), "a1").Tokens; got != 1000 {
		t.Fatalf("distinct event dropped: tokens %d, want 1000", got)
	}

	// Duplicate spawn does not renumber.
	sp := event.Event{Seq: 103, At: f.at(0), AgentID: "a1", Kind: event.AgentSpawned}
	f.m.Apply(sp, f.at(4*time.Second))
	if got := agentByID(f.m.Snapshot(), "a1").SpawnIndex; got != 1 {
		t.Fatalf("spawn index changed to %d on duplicate spawn, want 1", got)
	}
}

// TestVerifyAndRetry covers the §2.2 inferred-verify marker and the §2.3
// retry heuristic. Verify is inferred only from a check that completed
// successfully (§2.10: run-auth-tests earns the marker, fix-ts2345's failing
// typechecks never do).
func TestVerifyAndRetry(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")

	out := f.apply(1*time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm typecheck"))
	if hasKind(out, event.VerifyObserved) {
		t.Fatalf("VerifyObserved on ToolStart — nothing has completed yet: %v", out)
	}

	out = f.apply(2*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"), withErr("TS2345 handlers.ts:88"))
	if hasKind(out, event.VerifyObserved) {
		t.Fatalf("VerifyObserved for a FAILING typecheck: %v", out)
	}
	if agentByID(f.m.Snapshot(), "a1").Verified {
		t.Fatal("verified flag set by a failing check")
	}
	out = f.apply(3*time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm  typecheck")) // whitespace normalised
	if !hasKind(out, event.RetryInferred) {
		t.Fatalf("no RetryInferred after failing ToolEnd: %v", out)
	}
	out = f.apply(4500*time.Millisecond, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"))
	if !hasKind(out, event.VerifyObserved) {
		t.Fatalf("no VerifyObserved for a passing typecheck: %v", out)
	}
	if !agentByID(f.m.Snapshot(), "a1").Verified {
		t.Fatal("verified flag not set")
	}
	out = f.apply(5500*time.Millisecond, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"))
	if hasKind(out, event.VerifyObserved) {
		t.Fatal("VerifyObserved emitted twice")
	}
	if got := agentByID(f.m.Snapshot(), "a1").Retries; got != 1 {
		t.Fatalf("retries = %d, want 1", got)
	}

	f.apply(4*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"), withExit(1))
	out = f.apply(5*time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm typecheck"))
	if !hasKind(out, event.RetryInferred) {
		t.Fatal("second retry not inferred")
	}
	if got := agentByID(f.m.Snapshot(), "a1").Retries; got != 2 {
		t.Fatalf("retries = %d, want 2", got)
	}

	// Success clears the failure: a later identical run is not a retry.
	f.apply(6*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm typecheck"))
	out = f.apply(7*time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm typecheck"))
	if hasKind(out, event.RetryInferred) {
		t.Fatal("retry inferred after a successful run")
	}
	if got := agentByID(f.m.Snapshot(), "a1").LastError; got != "exit 1" {
		t.Fatalf("lastError = %q, want %q", got, "exit 1")
	}
}

// TestDropped covers C9 bookkeeping: derived kinds from a source, unknown
// kinds, and unresolvable agent references are counted, never applied.
func TestDropped(t *testing.T) {
	f := newFix()
	f.apply(0, event.StallDetected, "a1")                   // derived kind from a "source"
	f.apply(0, event.Kind(250), "a1")                       // unknown kind
	f.apply(0, event.KindInvalid, "a1")                     // invalid
	f.apply(0, event.ToolStart, "", withTool("Bash", "ls")) // no agent reference
	w := f.m.Snapshot()
	if w.DroppedEvents != 4 {
		t.Fatalf("DroppedEvents = %d, want 4", w.DroppedEvents)
	}
	if len(w.Agents) != 0 {
		t.Fatalf("dropped events created agents: %v", w.Agents)
	}
}

// TestTokens covers cumulative vs delta accounting (event.Event.TokensCum)
// and out-of-order cumulative reports.
func TestTokens(t *testing.T) {
	t.Run("delta", func(t *testing.T) {
		f := newFix()
		f.spawnRun(0, "a1")
		f.apply(1*time.Second, event.TokensUpdated, "a1", withTokens(100, false))
		f.apply(2*time.Second, event.TokensUpdated, "a1", withTokens(50, false))
		if got := agentByID(f.m.Snapshot(), "a1").Tokens; got != 150 {
			t.Fatalf("tokens = %d, want 150", got)
		}
	})
	t.Run("cumulative", func(t *testing.T) {
		f := newFix()
		f.spawnRun(0, "a1")
		f.apply(1*time.Second, event.TokensUpdated, "a1", withTokens(100, true))
		f.apply(2*time.Second, event.TokensUpdated, "a1", withTokens(250, true))
		f.apply(3*time.Second, event.TokensUpdated, "a1", withTokens(200, true)) // stale, out of order
		if got := agentByID(f.m.Snapshot(), "a1").Tokens; got != 250 {
			t.Fatalf("tokens = %d, want 250 (stale cumulative must not regress)", got)
		}
	})
}

// TestSparkBuckets covers §2.9/§3.4: buckets hold both token deltas and call
// counts, aligned to the machine clock.
func TestSparkBuckets(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.apply(0, event.ToolStart, "a1", withTool("Grep", "createUser"))
	f.apply(0, event.TokensUpdated, "a1", withTokens(500, false))
	a := agentByID(f.m.Snapshot(), "a1")
	if a.SparkBuckets[59].Calls != 1 || a.SparkBuckets[59].Tokens != 500 {
		t.Fatalf("newest bucket = %+v, want {Tokens:500 Calls:1}", a.SparkBuckets[59])
	}
	if !a.SparkAt.Equal(t0) {
		t.Fatalf("SparkAt = %v, want %v", a.SparkAt, t0)
	}
	f.tick(30 * time.Second)
	a = agentByID(f.m.Snapshot(), "a1")
	if a.SparkBuckets[29].Calls != 1 || a.SparkBuckets[29].Tokens != 500 {
		t.Fatalf("bucket did not shift with the clock: idx29 = %+v", a.SparkBuckets[29])
	}
	if a.SparkBuckets[59].Calls != 0 {
		t.Fatalf("stale data in the newest bucket: %+v", a.SparkBuckets[59])
	}
}

// TestOpenCall covers §2.6a: the open-call state the UI renders instead of a
// sparkline, plus the CallOpened/CallClosed derived events.
func TestOpenCall(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	out := f.apply(1*time.Second, event.ToolStart, "a1", withTool("Bash", "pnpm exec vitest run"))
	if !hasKind(out, event.CallOpened) {
		t.Fatalf("no CallOpened: %v", out)
	}
	a := agentByID(f.m.Snapshot(), "a1")
	if a.OpenCall == nil || a.OpenCall.Tool != "Bash" || !a.OpenCall.Since.Equal(f.at(time.Second)) {
		t.Fatalf("open call = %+v", a.OpenCall)
	}
	out = f.apply(3*time.Second, event.ToolEnd, "a1", withTool("Bash", "pnpm exec vitest run"))
	if !hasKind(out, event.CallClosed) {
		t.Fatalf("no CallClosed: %v", out)
	}
	a = agentByID(f.m.Snapshot(), "a1")
	if a.OpenCall != nil {
		t.Fatalf("open call not cleared: %+v", a.OpenCall)
	}
	if len(a.CallHistory) != 1 || a.TotalCalls != 1 {
		t.Fatalf("call history = %v (total %d), want 1 record", a.CallHistory, a.TotalCalls)
	}
	// An unmatched ToolEnd still records history but closes nothing.
	out = f.apply(4*time.Second, event.ToolEnd, "a1", withTool("Read", "x.go"))
	if hasKind(out, event.CallClosed) {
		t.Fatal("CallClosed emitted with no matching open call")
	}
	if got := agentByID(f.m.Snapshot(), "a1").TotalCalls; got != 2 {
		t.Fatalf("total calls = %d, want 2", got)
	}
}

func TestCallHistoryRing(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	for i := 0; i < 30; i++ {
		f.apply(time.Duration(i)*time.Second, event.ToolEnd, "a1", withTool("Read", "f.go"))
	}
	a := agentByID(f.m.Snapshot(), "a1")
	if len(a.CallHistory) != 20 {
		t.Fatalf("ring size = %d, want 20", len(a.CallHistory))
	}
	if a.TotalCalls != 30 {
		t.Fatalf("total calls = %d, want 30", a.TotalCalls)
	}
	if !a.CallHistory[19].At.Equal(f.at(29 * time.Second)) {
		t.Fatalf("newest record at %v, want %v", a.CallHistory[19].At, f.at(29*time.Second))
	}
}

func TestUnknownRevives(t *testing.T) {
	f := newFix()
	f.spawnRun(0, "a1")
	f.tick(601 * time.Second)
	if got := agentByID(f.m.Snapshot(), "a1").Status; got != StatusUnknown {
		t.Fatalf("status = %v, want unknown", got)
	}
	f.apply(602*time.Second, event.ToolStart, "a1", withTool("Read", "x.go"))
	if got := agentByID(f.m.Snapshot(), "a1").Status; got != StatusRun {
		t.Fatalf("status = %v, want run after new activity", got)
	}
}

// TestMainAgent covers the §2.2 exception: main shows its todo text, else
// activity, never pips.
func TestMainAgent(t *testing.T) {
	f := newFix()
	f.apply(0, event.ToolStart, event.MainAgentID, withTool("TodoWrite", ""), withDetail("write the migration"))
	f.apply(1*time.Second, event.ToolStart, event.MainAgentID, withTool("Bash", "go vet ./..."))
	f.apply(2*time.Second, event.TokensUpdated, event.MainAgentID, withTokens(112000, true))
	w := f.m.Snapshot()
	if w.Main.Todo != "write the migration" {
		t.Fatalf("todo = %q", w.Main.Todo)
	}
	if w.Main.Activity != "Bash go vet ./..." {
		t.Fatalf("activity = %q", w.Main.Activity)
	}
	if w.Main.Tokens != 112000 {
		t.Fatalf("tokens = %d", w.Main.Tokens)
	}
	if !w.Main.LastEventAt.Equal(f.at(2 * time.Second)) {
		t.Fatalf("lastEventAt = %v", w.Main.LastEventAt)
	}
	if len(w.Agents) != 0 {
		t.Fatal("main leaked into the subagent list")
	}
}

// TestMainTitle: Claude Code rewrites its own session title as the session
// evolves, so the last one seen wins — but "last" is Seq order, not arrival
// order. A late older line must not clobber a newer title.
func TestMainTitle(t *testing.T) {
	const (
		first  = "Connect to SND on IP 52.43.123.31"
		second = "Build agentpane — live TUI monitor for Claude Code subagents"
	)
	f := newFix()
	if got := f.m.Snapshot().Main.Title; got != "" {
		t.Fatalf("title invented before any ai-title line: %q", got)
	}
	f.apply(0, event.SessionTitled, event.MainAgentID, withDetail(first))
	if got := f.m.Snapshot().Main.Title; got != first {
		t.Fatalf("title = %q, want %q", got, first)
	}
	// Rewritten: last one wins.
	f.apply(time.Second, event.SessionTitled, event.MainAgentID, withDetail(second))
	if got := f.m.Snapshot().Main.Title; got != second {
		t.Fatalf("retitle ignored: title = %q, want %q", got, second)
	}
	// Out of order: an older Seq arriving late must not win.
	f.m.Apply(event.Event{
		Seq: 1, At: f.at(2 * time.Second), AgentID: event.MainAgentID,
		Kind: event.SessionTitled, Detail: first,
	}, f.at(2*time.Second))
	if got := f.m.Snapshot().Main.Title; got != second {
		t.Fatalf("older Seq overwrote a newer title: %q", got)
	}
	// Idempotent: the same title again is not a visible change.
	rev := f.m.Snapshot().Rev
	f.apply(3*time.Second, event.SessionTitled, event.MainAgentID, withDetail(second))
	if got := f.m.Snapshot().Rev; got != rev {
		t.Errorf("repeating the same title bumped Rev %d → %d", rev, got)
	}
	// An empty Detail never blanks a known title (C8: no invention, no
	// silent loss).
	f.apply(4*time.Second, event.SessionTitled, event.MainAgentID)
	if got := f.m.Snapshot().Main.Title; got != second {
		t.Errorf("empty SessionTitled blanked the title: %q", got)
	}
	// It is session metadata, not main's work: it must not move main's
	// liveness clock, its todo, or its activity line.
	w := f.m.Snapshot()
	if !w.Main.LastEventAt.IsZero() || w.Main.Todo != "" || w.Main.Activity != "" {
		t.Errorf("a retitle masqueraded as main activity: %+v", w.Main)
	}
	// Session-scoped like everything else: Accepts gates it.
	f.m.Reset("session-A")
	if got := f.m.Snapshot().Main.Title; got != "" {
		t.Fatalf("title survived Reset: %q", got)
	}
	f.apply(5*time.Second, event.SessionTitled, event.MainAgentID, withDetail("session B's title"),
		func(e *event.Event) { e.SessionID = "session-B" })
	if got := f.m.Snapshot().Main.Title; got != "" {
		t.Errorf("a foreign session's title landed on this pane's main row: %q", got)
	}
	f.apply(6*time.Second, event.SessionTitled, event.MainAgentID, withDetail(first),
		func(e *event.Event) { e.SessionID = "session-A" })
	if got := f.m.Snapshot().Main.Title; got != first {
		t.Errorf("own-session title dropped: %q", got)
	}
}

// TestNoPanicOnJunk is the C9 guarantee: malformed and partial events are
// absorbed without panicking.
func TestNoPanicOnJunk(t *testing.T) {
	f := newFix()
	f.apply(0, event.PermissionRequested, "a1") // no tool, no payload, no target
	f.apply(0, event.ToolEnd, "a1")             // no open call, empty tool/target
	f.apply(0, event.ToolEnd, "a2", withExit(3))
	f.apply(0, event.AgentMerged, "a3") // return before spawn
	f.apply(0, event.FileEdited, "a4")  // empty target
	f.apply(-time.Hour, event.ToolStart, "a5", withTool("", ""))
	f.apply(0, event.SessionEnd, "")
	f.apply(0, event.SessionIdle, "")
	f.m.Apply(event.Event{}, time.Time{}) // fully zero event, zero time
	f.tick(0)
	w := f.m.Snapshot()
	// a3 returned without ever spawning, being named, or doing anything, so
	// it has earned no row (§1.5, Machine.quiet) — but the out-of-order
	// return must still have been absorbed, not dropped.
	if a := agentByID(w, "a3"); a != nil {
		if a.Status != StatusDone {
			t.Fatalf("out-of-order return not tolerated: %+v", a)
		}
	} else if q := quietByID(w, "a3"); q == nil || q.Status != StatusDone {
		t.Fatalf("out-of-order return neither rendered nor counted: %+v", q)
	}
	if a := agentByID(w, "a2"); a == nil || a.LastError != "exit 3" {
		t.Fatalf("exit-code failure not recorded: %+v", a)
	}
}

// TestCrossSessionEventsIgnored: another session's registry rows (SessionIdle,
// NotificationRaised) and stale post-switch events must not mutate this world
// — a concurrent session going idle must never end the attached run.
func TestCrossSessionEventsIgnored(t *testing.T) {
	f := newFix()
	f.apply(0, event.SessionStart, event.MainAgentID, func(e *event.Event) { e.SessionID = "session-A" })
	f.apply(0, event.UserPromptSubmitted, event.MainAgentID, withDetail("refactor"))
	f.spawnRun(time.Second, "a1")
	f.apply(2*time.Second, event.AgentReturned, "a1")

	// Session B goes idle → must not flip A's state or end A's run.
	out := f.apply(3*time.Second, event.SessionIdle, event.MainAgentID,
		func(e *event.Event) { e.SessionID = "session-B" })
	if hasKind(out, event.RunEnded) {
		t.Fatal("session B's idle ended session A's run")
	}
	w := f.m.Snapshot()
	if w.Session.State != SessionActive || w.Run == nil {
		t.Fatalf("session B's idle mutated A: state=%v run=%v", w.Session.State, w.Run)
	}
	// Session B waiting on permission → must not touch A's main activity.
	f.apply(4*time.Second, event.NotificationRaised, event.MainAgentID,
		withDetail("registry: waiting on permission"),
		func(e *event.Event) { e.SessionID = "session-B" })
	if got := f.m.Snapshot().Main.Activity; got != "" {
		t.Fatalf("session B's permission wait leaked into A's activity: %q", got)
	}
	// A's own idle still ends the run.
	out = f.apply(5*time.Second, event.SessionIdle, event.MainAgentID,
		func(e *event.Event) { e.SessionID = "session-A" })
	if !hasKind(out, event.RunEnded) {
		t.Fatal("session A's own idle did not end the run")
	}
}

// TestReset: a session switch starts from a clean world pinned to the new
// session id — no stale agents, asks, or runs, and B's events apply cleanly
// even when their (AgentID, Kind, Seq) keys collide with A's.
func TestReset(t *testing.T) {
	f := newFix()
	f.apply(0, event.SessionStart, event.MainAgentID, func(e *event.Event) { e.SessionID = "session-A" })
	f.apply(0, event.UserPromptSubmitted, event.MainAgentID, withDetail("prompt A"))
	f.spawnRun(time.Second, "a1")
	f.apply(2*time.Second, event.PermissionRequested, "a1", withTool("Bash", "rm -rf x"), withAsk("command", "rm -rf x"))

	f.m.Reset("session-B")
	w := f.m.Snapshot()
	if len(w.Agents) != 0 || len(w.Asks) != 0 || w.Run != nil || w.LastRun != nil {
		t.Fatalf("stale state survived Reset: %+v", w)
	}
	if w.Session.ShortID != "sess" {
		t.Fatalf("session not pinned: %+v", w.Session)
	}
	// B's mux restarts numbering, so an exact (AgentID, Kind, Seq) triple A
	// already consumed arrives again — it must not be dropped as a duplicate.
	f.seq = 2 // A's AgentSpawned for "a1" was seq 3
	f.apply(10*time.Second, event.AgentSpawned, "a1", withName("agent a1"),
		func(e *event.Event) { e.SessionID = "session-B" })
	if a := agentByID(f.m.Snapshot(), "a1"); a == nil {
		t.Fatal("session B's event dropped as a duplicate of A's after Reset")
	}
	// A's stale in-flight events are ignored.
	f.apply(11*time.Second, event.SessionIdle, event.MainAgentID,
		func(e *event.Event) { e.SessionID = "session-A" })
	if got := f.m.Snapshot().Session.State; got == SessionIdle {
		t.Fatal("session A's stale idle applied after Reset")
	}
}

// TestMainTodoIgnoresToolEndDuration: the hooks PostToolUse Detail is the
// duration ("142ms") — it must never overwrite main's todo line, which only
// the ToolStart twin carries.
func TestMainTodoIgnoresToolEndDuration(t *testing.T) {
	f := newFix()
	f.apply(0, event.ToolStart, event.MainAgentID, withTool("TodoWrite", ""), withDetail("write the migration"))
	f.apply(time.Second, event.ToolEnd, event.MainAgentID, withTool("TodoWrite", ""), withDetail("142ms"))
	w := f.m.Snapshot()
	if w.Main.Todo != "write the migration" {
		t.Fatalf("todo = %q, want the ToolStart text (ToolEnd carries duration_ms)", w.Main.Todo)
	}
	if w.Main.Activity == "142ms" {
		t.Fatalf("TodoWrite duration leaked into main's activity")
	}
}

func TestSessionState(t *testing.T) {
	f := newFix()
	f.apply(0, event.SessionStart, "", func(e *event.Event) { e.SessionID = "3f9c1234-abcd" })
	w := f.m.Snapshot()
	if w.Session.State != SessionActive || w.Session.ShortID != "3f9c" {
		t.Fatalf("session = %+v", w.Session)
	}
	f.apply(1*time.Second, event.CompactStarted, "")
	if got := f.m.Snapshot().Session.State; got != SessionCompacting {
		t.Fatalf("state = %v, want compacting", got)
	}
	f.apply(2*time.Second, event.CompactFinished, "")
	if got := f.m.Snapshot().Session.State; got != SessionActive {
		t.Fatalf("state = %v, want active", got)
	}
	f.apply(3*time.Second, event.SessionIdle, "")
	if got := f.m.Snapshot().Session.State; got != SessionIdle {
		t.Fatalf("state = %v, want idle", got)
	}
	f.apply(4*time.Second, event.SessionEnd, "")
	if got := f.m.Snapshot().Session.State; got != SessionEnded {
		t.Fatalf("state = %v, want ended", got)
	}
	f.apply(5*time.Second, event.SourceDisconnected, "")
	if f.m.Snapshot().Connected {
		t.Fatal("still connected after SourceDisconnected")
	}
	f.apply(6*time.Second, event.SourceConnected, "")
	if !f.m.Snapshot().Connected {
		t.Fatal("not connected after SourceConnected")
	}
}
