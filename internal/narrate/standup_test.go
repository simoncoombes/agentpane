package narrate

import (
	"fmt"
	"regexp"
	"strconv"
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
//
// The asking agent is NOT one of them. It is the keystroke, not a queue behind
// it, and counting it made a lone ask claim "1 agent is parked behind that
// keystroke" with nothing whatsoever behind it — while making the ladder's
// "Nothing else is parked behind it" rung unreachable for as long as any ask
// existed. The two sentences that count this set — the rung and the risk line —
// are checked against each other at every size, because a frame that says three
// on one line and two on the next is worse than either number alone.
func TestWaitingLadderCountsAreDerived(t *testing.T) {
	for _, parked := range []int{0, 1, 2, 5} {
		w := waitingWorld(200*time.Second, parked)
		got := joined(Narrate(w, Memory{}, snapNow(), opt()).Standup)
		switch {
		case parked == 0:
			if !strings.Contains(got, "Nothing else is parked behind it") {
				t.Errorf("one ask and nothing queued: the rung does not say so, got:\n%s", got)
			}
			if strings.Contains(got, "parked behind that keystroke") {
				t.Errorf("the asker is counted as parked behind itself:\n%s", got)
			}
		case parked == 1:
			if !strings.Contains(got, "1 agent is parked behind that keystroke") {
				t.Errorf("parked=1: want the singular rung, got:\n%s", got)
			}
		default:
			if !strings.Contains(got, fmt.Sprintf("%d agents are parked behind that keystroke", parked)) {
				t.Errorf("parked=%d: want %d agents on the rung, got:\n%s", parked, parked, got)
			}
		}
		// The rung and the risk line count the same set, so they may never
		// disagree — including by one of them being absent.
		if rung, risk := parkedCount(got, "keystroke"), parkedCount(got, "answer"); rung != risk {
			t.Errorf("parked=%d: %d parked behind the keystroke but %d behind the answer:\n%s",
				parked, rung, risk, got)
		}
	}
}

// parkedCount reads the number out of "n agents are parked behind that <behind>",
// or -1 when that sentence is not in the text at all.
func parkedCount(text, behind string) int {
	m := regexp.MustCompile(`(\d+) agents? (?:is|are) parked behind that ` + behind).FindStringSubmatch(text)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// §3.7 gives the exact command to the band's own row, verbatim, and the standup's
// sentences may not print it a second time: the reader would be reading the same
// ask twice inside one region, four rows apart. The band is the only place the
// bytes appear in the standup, so nothing the composer writes carries a quoted
// command at all.
func TestTheStandupNeverQuotesTheCommand(t *testing.T) {
	worlds := scenes()
	worlds["resolving"] = resolvingWorld(true)
	for name, w := range worlds {
		st := Narrate(w, Memory{}, snapNow(), opt()).Standup
		got := joined(st)
		if strings.Contains(got, "`") {
			t.Errorf("%s: the standup quotes something verbatim:\n%s", name, got)
		}
		for _, ask := range w.Asks {
			if strings.Contains(got, ask.Command) {
				t.Errorf("%s: the standup restates the command %q:\n%s", name, ask.Command, got)
			}
		}
	}
	// And it still says what the ask is FOR: the kind of answer wanted, which is
	// the part the band's row does not carry.
	got := joined(Narrate(blockedWorld(), Memory{}, snapNow(), opt()).Standup)
	if !strings.Contains(got, "permission to run one Bash command") {
		t.Errorf("the paraphrase dropped what is being asked for:\n%s", got)
	}
}

// §3.7.8: Ask.Resolving means an answer was SEEN, not that the outcome is known.
// Telling that reader they are the blocker asks them to answer a prompt they have
// already answered, and the ladder's "it's one key" is pressure to press a key
// that has been pressed.
func TestResolvingAskIsNotTheReadersProblemAnyMore(t *testing.T) {
	st := Narrate(resolvingWorld(true), Memory{}, snapNow(), opt()).Standup
	got := joined(st)

	for _, banned := range []string{
		"You're the blocker", "It's one key", "parked behind that keystroke",
		"answer in the left pane", "nothing on this screen is going to move it but you",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("a resolving ask still says %q:\n%s", banned, got)
		}
	}
	// What is true instead, and marked as the reading of two events that it is.
	var line Line
	for _, l := range st.Lines {
		if l.Part == PartMust && strings.Contains(l.Text, "answer is in") {
			line = l
		}
	}
	if line.Text == "" {
		t.Fatalf("nothing says the answer is already in:\n%s", got)
	}
	if !line.Inferred || !strings.HasSuffix(line.Text, hedgeNoSignal+".") {
		t.Errorf("the resolving reading is not marked as an inference: %q (inferred=%v)",
			line.Text, line.Inferred)
	}
	if !strings.Contains(line.Text, "clears late") {
		t.Errorf("the line does not say why it is still on the band: %q", line.Text)
	}
	// The wait is still reported — it is a subtraction of two timestamps — and the
	// closing line says nothing needs doing rather than naming a key.
	if !strings.Contains(got, "3m20s waiting") {
		t.Errorf("the wait stopped being reported:\n%s", got)
	}
	if st.Action.Text != "▸ "+ActionResolving {
		t.Errorf("action = %q, want the resolving line", st.Action.Text)
	}

	// One unanswered ask and the keys come straight back: the reader is still the
	// blocker for THAT one, however answered the oldest is.
	two := Narrate(resolvingWorld(false), Memory{}, snapNow(), opt()).Standup
	if !strings.Contains(two.Action.Text, "answer in the left pane") {
		t.Errorf("a second, unanswered ask lost the keys: %q", two.Action.Text)
	}
	if !strings.Contains(joined(two), "1 more ask behind it") {
		t.Errorf("the second ask is not counted:\n%s", joined(two))
	}
}

// resolvingWorld: an ask whose answer has been seen but not confirmed. With
// `only` false a second, unanswered ask sits behind it.
func resolvingWorld(only bool) state.World {
	w := waitingWorld(200*time.Second, 1)
	w.Asks[0].Resolving = true
	if only {
		return w
	}
	second := agent("w2", "check the migration", state.StatusAsk, 4, 190)
	second.Tokens = 100
	w.Agents = append(w.Agents, second)
	w.Asks = append(w.Asks, state.Ask{
		Key: "k2", AgentID: "w2", Description: "check the migration", Tool: "Bash",
		Command: "psql -f db/migrations/0042.sql", Kind: "command",
		RaisedAt: snapNow().Add(-30 * time.Second), DupeCount: 1,
	})
	return w
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
