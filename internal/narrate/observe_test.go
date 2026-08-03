package narrate

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The filter, stated as a test: a tool call is never a sentence, and the six
// transitions that change an action or contradict a claim always are.
func TestCommentaryFilterKeepsTransitionsAndDropsToolCalls(t *testing.T) {
	w := blockedWorld()
	// Twenty tool calls on the busiest agent, none of which may produce a line.
	agents := append([]state.Agent(nil), w.Agents...)
	for i := 0; i < 20; i++ {
		agents[2].CallHistory = append(agents[2].CallHistory, state.Call{
			Tool: "Grep", Target: "createUser src/**", At: at(100 + i),
		})
	}
	agents[2].TotalCalls = 20
	w.Agents = agents

	base := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	withCalls := Narrate(w, Memory{}, snapNow(), opt())
	if len(base.New) != len(withCalls.New) {
		t.Errorf("20 tool calls changed the commentary: %d entries vs %d",
			len(withCalls.New), len(base.New))
	}
	for _, e := range withCalls.New {
		if strings.Contains(e.Text, "Grep") {
			t.Errorf("a tool call earned a sentence: %q", e.Text)
		}
	}

	// The transitions that must be there.
	want := []string{
		"Run opened",
		"asked for permission to run",
		"went quiet",
		"returned",
		"both started writing db/schema.ts",
		"check exited zero",
		"never did anything",
	}
	body := ""
	for _, e := range withCalls.New {
		body += e.Text + "\n"
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("commentary is missing %q:\n%s", s, body)
		}
	}
}

// One sentence per event, no matter how often the world is sampled. observe()
// re-derives every candidate on every frame, so this is the property that makes
// the region usable at 10fps.
func TestEachEventIsAppendedExactlyOnce(t *testing.T) {
	w := blockedWorld()
	mem := Memory{}
	var first int
	for i := 0; i < 30; i++ {
		r := Narrate(w, mem, snapNow().Add(time.Duration(i)*100*time.Millisecond), opt())
		mem = r.Memory
		if i == 0 {
			first = len(r.New)
			continue
		}
		if len(r.New) != 0 {
			t.Fatalf("frame %d appended %d duplicate entries: %+v", i, len(r.New), r.New)
		}
	}
	if first == 0 {
		t.Fatal("first frame appended nothing")
	}
	if len(mem.Entries) != first {
		t.Errorf("memory holds %d entries after 30 frames, want %d", len(mem.Entries), first)
	}
}

// The commentary is ordered by the event's own time, not by when it was noticed,
// so one pass over a finished world still reads as a history.
func TestCommentaryIsOrderedByEventTime(t *testing.T) {
	r := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	if len(r.New) < 5 {
		t.Fatalf("only %d entries", len(r.New))
	}
	for i := 1; i < len(r.New); i++ {
		if r.New[i].At < r.New[i-1].At {
			t.Errorf("entry %d (%s) precedes %d (%s)", i, clock(r.New[i].At), i-1, clock(r.New[i-1].At))
		}
	}
	if r.New[0].At != 0 || !strings.HasPrefix(r.New[0].Text, "Run opened") {
		t.Errorf("first entry is %q at %s, want the run opening at 0:00", r.New[0].Text, clock(r.New[0].At))
	}
}

// The narrator marks its own bad calls (§13.2). Writing an agent off and then
// watching it return is the canonical case, and the correction must name itself
// rather than quietly logging a return.
func TestSelfCorrectionWhenTheStateContradictsAnEarlierLine(t *testing.T) {
	first := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	wroteOff := false
	for _, e := range first.New {
		if strings.Contains(e.Text, "went quiet") {
			wroteOff = true
		}
	}
	if !wroteOff {
		t.Fatal("the first frame never wrote the quiet agent off")
	}

	second := Narrate(clearWorld(), first.Memory, at(260), opt())
	var mine []Entry
	for _, e := range second.New {
		if e.Mine {
			mine = append(mine, e)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("want exactly one self-correction, got %d: %+v", len(mine), mine)
	}
	if !strings.Contains(mine[0].Text, "That one's mine") {
		t.Errorf("correction does not own itself: %q", mine[0].Text)
	}
	if !strings.Contains(mine[0].Text, "check-typecheck-works") {
		t.Errorf("correction does not name the agent it was wrong about: %q", mine[0].Text)
	}

	// And it is not repeated on the next frame.
	third := Narrate(clearWorld(), second.Memory, at(300), opt())
	for _, e := range third.New {
		if e.Mine {
			t.Errorf("self-correction repeated: %q", e.Text)
		}
	}
}

// An agent that returns without ever having been written off gets no
// self-correction: the narrator only apologises for lines it actually wrote.
func TestNoSelfCorrectionWithoutAnEarlierClaim(t *testing.T) {
	r := Narrate(clearWorld(), Memory{}, snapNow(), opt())
	for _, e := range r.New {
		if e.Mine {
			t.Errorf("apologised for a line it never wrote: %q", e.Text)
		}
	}
}

// Session-level states are reported on the transition, not per frame: a world
// that stays disconnected for a minute produces one line, and a second drop
// after a reconnect produces another.
func TestSessionTransitionsAreReportedOnTheEdge(t *testing.T) {
	w := blockedWorld()
	w.Connected = false
	mem := Memory{}
	for i := 0; i < 5; i++ {
		r := Narrate(w, mem, snapNow().Add(time.Duration(i)*time.Second), opt())
		mem = r.Memory
	}
	drops := countContaining(mem.Entries, "event source dropped")
	if drops != 1 {
		t.Errorf("five disconnected frames produced %d drop lines, want 1", drops)
	}

	up := blockedWorld()
	r := Narrate(up, mem, at(300), opt())
	if countContaining(r.New, "reconnected") != 1 {
		t.Errorf("reconnect not reported: %+v", r.New)
	}
	down := Narrate(w, r.Memory, at(360), opt())
	if countContaining(down.New, "event source dropped") != 1 {
		t.Errorf("second drop was swallowed by the first one's key: %+v", down.New)
	}
}

// The first frame of a session is not a transition. A connected world must not
// announce a reconnection it never lost.
func TestFirstFrameIsNotATransition(t *testing.T) {
	r := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	if n := countContaining(r.New, "reconnected"); n != 0 {
		t.Errorf("announced %d reconnections on the first frame", n)
	}
	if n := countContaining(r.New, "dropped"); n != 0 {
		t.Errorf("announced %d drops on a connected first frame", n)
	}
}

// The commentary ring is bounded and trims the oldest first — the only direction
// a log can be cut without lying about where it starts.
func TestCommentaryRingIsBounded(t *testing.T) {
	mem := Memory{}
	o := Options{Name: testNames, Cap: 6}
	// Each frame introduces a new agent, so each frame adds at least one entry.
	w := emptyWorld()
	w.Main.Title = "one thing"
	for i := 0; i < 20; i++ {
		w.Agents = append(w.Agents, state.Agent{
			ID: string(rune('a' + i)), Name: "job number " + string(rune('a'+i)), NameExact: true,
			Status: state.StatusRun, SpawnedAt: at(10 + i), LastEventAt: at(10 + i), SpawnIndex: i,
		})
		mem = Narrate(w, mem, at(200+i), o).Memory
	}
	if len(mem.Entries) > 6 {
		t.Errorf("ring holds %d entries with Cap 6", len(mem.Entries))
	}
	if strings.Contains(mem.Entries[0].Text, "Run opened") {
		t.Errorf("trimmed from the wrong end: the run opening survived %d frames", 20)
	}
}

// Named is what arms §13.1(a): a slug the reader has seen in the standup must be
// frozen, so exactly the ids whose names appear in the text come back.
func TestNamedListsEveryAgentTheTextNames(t *testing.T) {
	r := Narrate(blockedWorld(), Memory{}, snapNow(), opt())
	body := joined(r.Standup)
	for _, e := range r.New {
		body += "\n" + e.Text
	}
	seen := map[string]bool{}
	for _, id := range r.Named {
		seen[id] = true
	}
	for _, a := range blockedWorld().Agents {
		slug := testNames(a)
		if strings.Contains(body, slug) && !seen[a.ID] {
			t.Errorf("%s (%s) is named in the text but not in Named", a.ID, slug)
		}
		if !strings.Contains(body, slug) && seen[a.ID] {
			t.Errorf("%s (%s) is in Named but never named in the text", a.ID, slug)
		}
	}
}

func countContaining(es []Entry, s string) int {
	n := 0
	for _, e := range es {
		if strings.Contains(e.Text, s) {
			n++
		}
	}
	return n
}
