package state

import (
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

func histBase() time.Time { return time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC) }

// histMachine feeds one agent a token delta every `every` for `span`, then ticks
// to the end. It is the shape of a real run: activity spread across a lifetime
// longer than the 60s spark window.
func histMachine(t *testing.T, span, every time.Duration) (*Machine, Agent) {
	t.Helper()
	m := New(DefaultConfig())
	start := histBase()
	m.Apply(event.Event{Kind: event.AgentSpawned, AgentID: "a1", AgentName: "port the routes", At: start}, start)
	total := 0
	for d := time.Duration(0); d <= span; d += every {
		at := start.Add(d)
		total += 100
		m.Apply(event.Event{Kind: event.TokensUpdated, AgentID: "a1", Tokens: total, At: at}, at)
	}
	end := start.Add(span)
	m.Tick(end)
	w := m.Snapshot()
	for _, a := range w.Agents {
		if a.ID == "a1" {
			return m, a
		}
	}
	t.Fatal("agent a1 never earned a row")
	return nil, Agent{}
}

// The ruling is "the whole agent lifetime". A five-minute run is five times the
// spark window, so this is the case the rolling window could not draw at all.
func TestHistoryCoversTheWholeLifetime(t *testing.T) {
	_, a := histMachine(t, 5*time.Minute, 5*time.Second)
	if len(a.HistoryBuckets) == 0 {
		t.Fatal("no lifetime series")
	}
	if a.HistoryStep != histStepSeconds*time.Second {
		t.Errorf("step = %s, want %ds for a run this short", a.HistoryStep, histStepSeconds)
	}
	if !a.HistoryFrom.Equal(histFloor(a.SpawnedAt, histStepSeconds)) {
		t.Errorf("series starts at %s, want the spawn bucket %s",
			a.HistoryFrom, histFloor(a.SpawnedAt, histStepSeconds))
	}
	// The span covered has to reach the agent's own clock, not the last event's.
	covered := time.Duration(len(a.HistoryBuckets)) * a.HistoryStep
	lifetime := a.LastEventAt.Sub(a.SpawnedAt)
	if covered < lifetime {
		t.Errorf("series covers %s of a %s lifetime — still a tail", covered, lifetime)
	}
	if covered > lifetime+2*a.HistoryStep {
		t.Errorf("series covers %s, overshooting a %s lifetime", covered, lifetime)
	}
	// And every token is accounted for: a lifetime chart that loses work is
	// worse than no chart.
	sum := 0
	for _, b := range a.HistoryBuckets {
		sum += b.Tokens
	}
	if sum != a.Tokens {
		t.Errorf("series holds %d tokens, the agent has %d", sum, a.Tokens)
	}
}

// Bounded, and bounded by halving resolution rather than dropping the oldest
// bucket: the span is the thing that must survive, because clipping the span is
// the failure the series exists to fix.
func TestHistoryIsBoundedAndCoarsensInsteadOfClipping(t *testing.T) {
	// histCap buckets at the base step covers histCap*10s; go well past it.
	span := time.Duration(histCap*histStepSeconds*4) * time.Second
	_, a := histMachine(t, span, 20*time.Second)
	if len(a.HistoryBuckets) > histCap {
		t.Errorf("series holds %d buckets, cap is %d", len(a.HistoryBuckets), histCap)
	}
	if a.HistoryStep <= histStepSeconds*time.Second {
		t.Errorf("step = %s: a %s run cannot fit the cap at the base granularity", a.HistoryStep, span)
	}
	if a.HistoryStep%(histStepSeconds*time.Second) != 0 {
		t.Errorf("step %s is not a multiple of the base bucket", a.HistoryStep)
	}
	if !a.HistoryFrom.Equal(histFloor(a.SpawnedAt, histStepSeconds)) {
		t.Errorf("coarsening moved the start to %s", a.HistoryFrom)
	}
	covered := time.Duration(len(a.HistoryBuckets)) * a.HistoryStep
	if covered < span {
		t.Errorf("series covers %s of a %s run — coarsening lost span", covered, span)
	}
	sum := 0
	for _, b := range a.HistoryBuckets {
		sum += b.Tokens
	}
	if sum != a.Tokens {
		t.Errorf("coarsening lost work: %d tokens in the series, %d on the agent", sum, a.Tokens)
	}
}

// Silence inside a lifetime is a measurement. An agent that went quiet two minutes
// ago has two minutes of empty buckets, and the series still reaches `now` — but a
// SETTLED agent stops at its return rather than growing a tail for ever.
func TestHistoryExtendsToTheAgentsOwnEdge(t *testing.T) {
	m := New(DefaultConfig())
	start := histBase()
	m.Apply(event.Event{Kind: event.AgentSpawned, AgentID: "a1", At: start}, start)
	m.Apply(event.Event{Kind: event.TokensUpdated, AgentID: "a1", Tokens: 500, At: start.Add(5 * time.Second)},
		start.Add(5*time.Second))

	quietFor := 2 * time.Minute
	m.Tick(start.Add(5*time.Second + quietFor))
	a := agentIn(t, m.Snapshot(), "a1")
	covered := time.Duration(len(a.HistoryBuckets)) * a.HistoryStep
	if covered < quietFor {
		t.Errorf("a live agent quiet for %s covers only %s", quietFor, covered)
	}
	trailing := 0
	for i := len(a.HistoryBuckets) - 1; i >= 0; i-- {
		if a.HistoryBuckets[i].Tokens != 0 || a.HistoryBuckets[i].Calls != 0 {
			break
		}
		trailing++
	}
	if trailing == 0 {
		t.Error("the quiet minutes are not on the chart")
	}

	// Settled: the series stops at the return.
	doneAt := start.Add(10 * time.Second)
	m.Apply(event.Event{Kind: event.AgentReturned, AgentID: "a1", At: doneAt}, doneAt)
	m.Tick(start.Add(20 * time.Minute))
	a = agentIn(t, m.Snapshot(), "a1")
	covered = time.Duration(len(a.HistoryBuckets)) * a.HistoryStep
	if covered > time.Minute {
		t.Errorf("a settled agent's series covers %s, past its own return", covered)
	}
}

// An event older than the series start is filed where it belongs, not folded into
// bucket 0 — the machine tolerates out-of-order arrival everywhere else.
func TestHistoryFilesOutOfOrderEventsInTheRightBucket(t *testing.T) {
	m := New(DefaultConfig())
	start := histBase()
	// First activity at +40s, so the series starts at the spawn bucket.
	m.Apply(event.Event{Kind: event.AgentSpawned, AgentID: "a1", At: start}, start)
	late := start.Add(40 * time.Second)
	m.Apply(event.Event{Kind: event.TokensUpdated, AgentID: "a1", Tokens: 100, At: late}, late)
	// Then a call whose timestamp is older than everything seen so far.
	early := start.Add(5 * time.Second)
	m.Apply(event.Event{Kind: event.ToolStart, AgentID: "a1", Tool: "Bash", Target: "make", At: early}, late)
	m.Tick(late)

	a := agentIn(t, m.Snapshot(), "a1")
	i := int(early.Sub(a.HistoryFrom) / a.HistoryStep)
	if i < 0 || i >= len(a.HistoryBuckets) {
		t.Fatalf("bucket %d is outside a %d-bucket series", i, len(a.HistoryBuckets))
	}
	if a.HistoryBuckets[i].Calls != 1 {
		t.Errorf("the out-of-order call landed elsewhere: bucket %d has %+v",
			i, a.HistoryBuckets[i])
	}
	j := int(late.Sub(a.HistoryFrom) / a.HistoryStep)
	if a.HistoryBuckets[j].Tokens != 100 {
		t.Errorf("the later tokens moved: bucket %d has %+v", j, a.HistoryBuckets[j])
	}
}

// A World is a copy: mutating a snapshot's series must never reach the machine,
// or one paint could corrupt the next.
func TestHistorySnapshotIsACopy(t *testing.T) {
	m, a := histMachine(t, 90*time.Second, 10*time.Second)
	if len(a.HistoryBuckets) == 0 {
		t.Fatal("no series")
	}
	a.HistoryBuckets[0] = SparkBucket{Tokens: 999999}
	again := agentIn(t, m.Snapshot(), "a1")
	if again.HistoryBuckets[0].Tokens == 999999 {
		t.Error("the snapshot aliases machine state")
	}
}

// An agent that never did anything has no series at all: an honest absence, which
// is different information from a measured flatline (C8).
func TestHistoryIsAbsentWhenNothingWasEverRecorded(t *testing.T) {
	m := New(DefaultConfig())
	start := histBase()
	m.Apply(event.Event{Kind: event.AgentSpawned, AgentID: "a1", At: start}, start)
	m.Apply(event.Event{Kind: event.PermissionRequested, AgentID: "a1", Tool: "Bash",
		Ask: &event.AskPayload{Kind: "command", Command: "rm -rf x"}, At: start}, start)
	m.Tick(start.Add(time.Minute))
	a := agentIn(t, m.Snapshot(), "a1")
	if len(a.HistoryBuckets) != 0 || !a.HistoryFrom.IsZero() || a.HistoryStep != 0 {
		t.Errorf("fabricated a series for an agent with no activity: %d buckets from %s step %s",
			len(a.HistoryBuckets), a.HistoryFrom, a.HistoryStep)
	}
}

// histCoarsen is the memory bound's whole mechanism: pairs fold, the step doubles,
// bucket 0 keeps its start, and no work is lost.
func TestHistCoarsenPreservesWorkAndStart(t *testing.T) {
	in := []SparkBucket{{Tokens: 1}, {Tokens: 2}, {Calls: 3}, {Calls: 4}, {Tokens: 5}}
	out, step := histCoarsen(append([]SparkBucket(nil), in...), 10)
	if step != 20 {
		t.Errorf("step = %d, want 20", step)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	wantTok, wantCalls := 0, 0
	for _, b := range in {
		wantTok += b.Tokens
		wantCalls += b.Calls
	}
	gotTok, gotCalls := 0, 0
	for _, b := range out {
		gotTok += b.Tokens
		gotCalls += b.Calls
	}
	if gotTok != wantTok || gotCalls != wantCalls {
		t.Errorf("folded to %d tokens / %d calls, want %d / %d", gotTok, gotCalls, wantTok, wantCalls)
	}
	if out[0].Tokens != 3 {
		t.Errorf("bucket 0 = %+v, want the first pair summed", out[0])
	}
	if out[2].Tokens != 5 {
		t.Errorf("the odd tail bucket = %+v, want it carried", out[2])
	}
}

func agentIn(t *testing.T, w World, id string) Agent {
	t.Helper()
	for _, a := range w.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no agent %s in the world", id)
	return Agent{}
}
