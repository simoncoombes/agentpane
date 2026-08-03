package narrate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// §13.2's order is fixed — what you must do, what is at risk, the state — and it
// is the whole reason the region is worth reading. Any world, any scene: the
// parts never interleave and never arrive out of sequence.
func TestStandupOrderIsFixed(t *testing.T) {
	for name, w := range scenes() {
		st := Narrate(w, Memory{}, snapNow(), opt()).Standup
		last := Part(0)
		for i, l := range st.All() {
			if l.Part < last {
				t.Errorf("%s: line %d is %s after %s:\n  %s", name, i, l.Part, last, l.Text)
			}
			last = l.Part
		}
		if st.All()[len(st.All())-1].Part != PartAction {
			t.Errorf("%s: standup does not end on the action", name)
		}
	}
}

// The standup ALWAYS ends by naming the action or explicitly saying none is
// needed (§13.2). There is no scene in which the closing line is absent, and no
// scene in which it is vague: it either points at keys or says the words.
func TestStandupAlwaysClosesOnAnAction(t *testing.T) {
	for name, w := range scenes() {
		st := Narrate(w, Memory{}, snapNow(), opt()).Standup
		a := st.Action
		if a.Text == "" {
			t.Fatalf("%s: no action line", name)
		}
		if a.Part != PartAction {
			t.Errorf("%s: action carries part %s", name, a.Part)
		}
		if !strings.HasPrefix(a.Text, "▸ ") {
			t.Errorf("%s: action does not lead with ▸: %q", name, a.Text)
		}
		named := strings.Contains(a.Text, "⏎") || strings.Contains(a.Text, " y ") ||
			strings.Contains(a.Text, " o ")
		none := strings.Contains(a.Text, "nothing needs you")
		if !named && !none {
			t.Errorf("%s: action neither names an action nor says none is needed: %q", name, a.Text)
		}
	}
}

// Every sentence that goes further than the events do marks itself (§3.14, C8).
// The hedge is appended by infer(), so this test is really asking whether any
// call site built an Inferred line by hand.
func TestEveryInferenceIsHedged(t *testing.T) {
	check := func(where, text string, inferred bool) {
		hedged := false
		for _, h := range Hedges() {
			if strings.HasSuffix(text, h+".") {
				hedged = true
				break
			}
		}
		if inferred && !hedged {
			t.Errorf("%s: inferred line carries no hedge: %q", where, text)
		}
		if !inferred && hedged {
			t.Errorf("%s: hedged line is not marked Inferred: %q", where, text)
		}
	}
	for name, w := range scenes() {
		r := Narrate(w, Memory{}, snapNow(), opt())
		for _, l := range r.Standup.All() {
			check(name+" standup", l.Text, l.Inferred)
		}
		for _, e := range r.Memory.Entries {
			check(name+" commentary", e.Text, e.Inferred)
		}
	}
	// The self-correction path only exists across two frames.
	r1 := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	r2 := Narrate(clearWorld(), r1.Memory, at(260), opt())
	for _, e := range r2.New {
		check("second frame", e.Text, e.Inferred)
	}
}

// The waiting ladder, rung by rung (§13.2). Each threshold is checked on both
// sides, and each rung's numbers are checked against the world rather than
// against a literal in the assertion.
func TestWaitingLadderEscalatesWithDuration(t *testing.T) {
	cases := []struct {
		waited time.Duration
		want   string
	}{
		{0, "Nothing's lost yet."},
		{59 * time.Second, "Nothing's lost yet."},
		{60 * time.Second, "It's one key"},
		{149 * time.Second, "It's one key"},
		{150 * time.Second, "parked behind that keystroke"},
		{299 * time.Second, "parked behind that keystroke"},
		{300 * time.Second, "longer waiting on you than doing anything else"},
		{20 * time.Minute, "longer waiting on you than doing anything else"},
	}
	for _, c := range cases {
		// The run is exactly as long as the wait plus the ask agent's head start,
		// so the last rung's comparison is true; the false branch is covered
		// separately below.
		w := waitingWorld(c.waited, 2)
		st := Narrate(w, Memory{}, snapNow(), opt()).Standup
		got := joined(st)
		if !strings.Contains(got, c.want) {
			t.Errorf("waited %s: want rung %q, got:\n%s", c.waited, c.want, got)
		}
	}
}

// The third rung's count is derived from the world, not written into the
// template: change the number of parked agents and the sentence changes with it,
// including its grammar.
func TestWaitingLadderCountsAreDerived(t *testing.T) {
	for _, parked := range []int{0, 1, 2, 5} {
		w := waitingWorld(200*time.Second, parked)
		got := joined(Narrate(w, Memory{}, snapNow(), opt()).Standup)
		// parked = the asking agent + the queued ones.
		want := parked + 1
		switch {
		case want == 1:
			if !strings.Contains(got, "1 agent is parked") {
				t.Errorf("parked=%d: want singular, got:\n%s", parked, got)
			}
		default:
			if !strings.Contains(got, fmt.Sprintf("%d agents are parked", want)) {
				t.Errorf("parked=%d: want %d agents, got:\n%s", parked, want, got)
			}
		}
	}
}

// The last rung claims the run has spent longer waiting than working. That is an
// arithmetic claim, so it is only made when the arithmetic holds: a six-minute
// wait inside a one-hour run gets the other sentence.
func TestLastRungOnlyClaimsWhatTheClockSupports(t *testing.T) {
	long := waitingWorld(6*time.Minute, 1)
	long.Run.StartedAt = snapNow().Add(-time.Hour)
	got := joined(Narrate(long, Memory{}, snapNow(), opt()).Standup)
	if strings.Contains(got, "longer waiting on you than") {
		t.Errorf("claimed the wait dominated a one-hour run:\n%s", got)
	}
	if !strings.Contains(got, "on one keystroke") {
		t.Errorf("no fallback sentence for the last rung:\n%s", got)
	}

	short := waitingWorld(6*time.Minute, 1)
	short.Run.StartedAt = snapNow().Add(-7 * time.Minute)
	got = joined(Narrate(short, Memory{}, snapNow(), opt()).Standup)
	if !strings.Contains(got, "longer waiting on you than") {
		t.Errorf("did not claim a 6-of-7-minute wait:\n%s", got)
	}
}

// Same World twice → identical text. This is what makes the region snapshot
// testable and what stops a 10fps repaint from rewording itself.
func TestDeterministicForOneWorld(t *testing.T) {
	for name, w := range scenes() {
		a := Narrate(w, Memory{}, snapNow(), opt())
		b := Narrate(w, Memory{}, snapNow(), opt())
		if joined(a.Standup) != joined(b.Standup) {
			t.Errorf("%s: standup differs between two calls\n--- a ---\n%s\n--- b ---\n%s",
				name, joined(a.Standup), joined(b.Standup))
		}
		if len(a.New) != len(b.New) {
			t.Fatalf("%s: %d entries then %d", name, len(a.New), len(b.New))
		}
		for i := range a.New {
			if a.New[i] != b.New[i] {
				t.Errorf("%s: entry %d differs:\n  %+v\n  %+v", name, i, a.New[i], b.New[i])
			}
		}
		if strings.Join(a.Named, ",") != strings.Join(b.Named, ",") {
			t.Errorf("%s: Named differs: %v vs %v", name, a.Named, b.Named)
		}
	}
}

// Narrate must not mutate the Memory it was handed, or re-narrating an earlier
// frame would silently produce different text (and the ui's frame cache would be
// wrong about what it already showed).
func TestNarrateDoesNotMutateThePriorMemory(t *testing.T) {
	first := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	before := len(first.Memory.Entries)
	second := Narrate(clearWorld(), first.Memory, at(260), opt())
	if len(first.Memory.Entries) != before {
		t.Errorf("prior memory grew from %d to %d entries", before, len(first.Memory.Entries))
	}
	if len(second.Memory.Entries) <= before {
		t.Errorf("next memory did not grow: %d vs %d", len(second.Memory.Entries), before)
	}
	// Re-running the first frame against the untouched memory reproduces it.
	again := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	if joined(again.Standup) != joined(first.Standup) {
		t.Errorf("first frame is not reproducible")
	}
}

// Honest absence: a world with nothing in it says so, in every part, and does
// not borrow "all clear" from a run that never started.
func TestHonestAbsenceWhenThereIsNothingToSay(t *testing.T) {
	r := Narrate(emptyWorld(), Memory{}, snapNow(), opt())
	if r.Standup.Chip != ChipEmpty {
		t.Errorf("empty world got chip %q", r.Standup.Chip)
	}
	if got := joined(r.Standup); !strings.Contains(got, "Nothing's reported yet") ||
		!strings.Contains(got, "No agents on the tree") {
		t.Errorf("empty standup is not honest about the absence:\n%s", got)
	}
	if len(r.New) != 0 {
		t.Errorf("empty world produced %d commentary entries: %+v", len(r.New), r.New)
	}
	if len(r.Named) != 0 {
		t.Errorf("empty world named %v", r.Named)
	}
	// Every part is still present — an omitted part reads as a bug, not as
	// "nothing to report".
	seen := map[Part]bool{}
	for _, l := range r.Standup.All() {
		seen[l.Part] = true
	}
	for _, p := range []Part{PartMust, PartRisk, PartState, PartAction} {
		if !seen[p] {
			t.Errorf("empty standup omits the %s part", p)
		}
	}
}

// A run with a stalled agent and nothing else must not claim a cause it cannot
// see: no "it failed", no "it crashed", and no verdict of any kind (§2.3, C8).
func TestNeverAssertsAFailureThePlatformCannotReport(t *testing.T) {
	banned := []string{"failed", "crashed", "died", "broke", "succeeded", "success"}
	for name, w := range scenes() {
		r := Narrate(w, Memory{}, snapNow(), opt())
		body := joined(r.Standup)
		for _, e := range r.Memory.Entries {
			body += "\n" + e.Text
		}
		low := strings.ToLower(body)
		for _, b := range banned {
			// "verify failed" is a real exit code and is allowed; a bare verdict
			// about an agent is not.
			if strings.Contains(low, " "+b) && !strings.Contains(low, "check exited") {
				t.Errorf("%s: prose contains a verdict %q:\n%s", name, b, body)
			}
		}
	}
}

// The chip follows the same precedence as the alert band: an ask outranks
// anything worth watching, which outranks all clear.
func TestChipPrecedence(t *testing.T) {
	want := map[string]string{
		"blocked": ChipAlert,
		"risky":   ChipWatch,
		"clear":   ChipClear,
		"empty":   ChipEmpty,
	}
	for name, w := range scenes() {
		got := Narrate(w, Memory{}, snapNow(), opt()).Standup.Chip
		if got != want[name] {
			t.Errorf("%s: chip %q, want %q", name, got, want[name])
		}
	}
}

// A collision whose writers have all returned is history, not a live risk: the
// chip drops to ALL CLEAR and the sentence changes tense, but the run is still
// told that both edits landed by luck.
func TestResolvedCollisionStopsBeingALiveRisk(t *testing.T) {
	got := joined(Narrate(clearWorld(), Memory{}, snapNow(), opt()).Standup)
	if strings.Contains(got, "are both editing") {
		t.Errorf("resolved collision still described in the present tense:\n%s", got)
	}
	if !strings.Contains(got, hedgeOrdering) {
		t.Errorf("resolved collision does not separate luck from coordination:\n%s", got)
	}
}

// waitingWorld builds a minimal alert world: one ask that has been outstanding
// for `waited`, plus `parked` queued agents behind it.
func waitingWorld(waited time.Duration, parked int) state.World {
	now := snapNow()
	asker := state.Agent{
		ID: "w1", Name: "fix the thing", NameExact: true, Status: state.StatusAsk,
		SpawnedAt: now.Add(-waited - 30*time.Second), LastEventAt: now.Add(-waited),
	}
	agents := []state.Agent{asker}
	for i := 0; i < parked; i++ {
		agents = append(agents, state.Agent{
			ID: fmt.Sprintf("q%d", i), Name: fmt.Sprintf("queued job %d", i), NameExact: true,
			Status: state.StatusQueued, SpawnedAt: now.Add(-waited), LastEventAt: now.Add(-waited),
			SpawnIndex: i + 1,
		})
	}
	return state.World{
		Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:    state.Main{Title: "one thing", LastEventAt: now},
		Agents:  agents,
		Asks: []state.Ask{{
			Key: "k", AgentID: "w1", Description: "fix the thing", Tool: "Bash",
			Command: "pnpm rebuild", Kind: "command", RaisedAt: now.Add(-waited), DupeCount: 1,
		}},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-waited - 30*time.Second)},
		Connected: true,
	}
}

// Compose is the render path's entry point and Narrate is the ingest path's.
// They must agree exactly, or the standup the reader sees would depend on which
// function happened to be called for that frame.
func TestComposeMatchesNarrate(t *testing.T) {
	for name, w := range scenes() {
		full := Narrate(w, Memory{}, snapNow(), opt()).Standup
		only := Compose(w, Memory{}, snapNow(), opt())
		if joined(full) != joined(only) {
			t.Errorf("%s: Compose and Narrate disagree\n--- Compose ---\n%s\n--- Narrate ---\n%s",
				name, joined(only), joined(full))
		}
		if only.Chip != full.Chip || only.Clock != full.Clock || only.Scene != full.Scene {
			t.Errorf("%s: chip/clock/scene differ: %+v vs %+v", name, only, full)
		}
	}
	// And Compose must not append: a render that could grow the log is a render
	// that can grow it twice.
	first := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	before := len(first.Memory.Entries)
	Compose(clearWorld(), first.Memory, at(260), opt())
	if len(first.Memory.Entries) != before {
		t.Errorf("Compose appended to the memory: %d → %d", before, len(first.Memory.Entries))
	}
}
