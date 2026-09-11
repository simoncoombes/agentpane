package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// segsPlain is the unstyled text of a line, for assertions about content rather
// than colour.
func segsPlain(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.t())
	}
	return b.String()
}

// colNow is a fixed instant for the column tests.
func colNow() time.Time { return time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC) }

// producer is an agent with tokens in the trailing window: a row whose rate is
// worth showing.
func producer(id string, now time.Time, tokens int) state.Agent {
	a := state.Agent{ID: id, Status: state.StatusRun, Station: 2, LastEventAt: now}
	a.SparkBuckets[59] = state.SparkBucket{Tokens: tokens, Calls: 1}
	return a
}

// TestRightColumnMatrix is the §13.3 Q2 rule, one case per row state: rate for a
// producing agent, the open-call timer for a call past the dwell, the silence
// timer for anything stuck or ask, and nothing at all for a settled row.
func TestRightColumnMatrix(t *testing.T) {
	now := colNow()
	pal := NewPalette(2, false)
	v := newUIState(testConfig()) // RightCol defaults to "rate"

	openCall := producer("open", now, 6000)
	openCall.OpenCall = &state.OpenCall{Tool: "Bash", Since: now.Add(-3 * time.Minute)}

	stuck := producer("stuck", now.Add(-2*time.Minute-50*time.Second), 0)
	stuck.Status = state.StatusStuck
	stuck.LastEventAt = now.Add(-2*time.Minute - 50*time.Second)

	ask := producer("ask", now, 0)
	ask.Status = state.StatusAsk
	ask.LastEventAt = now.Add(-52 * time.Second)

	done := producer("done", now, 4000)
	done.Status = state.StatusDone
	done.DoneAt = now.Add(-20 * time.Second)

	queued := state.Agent{ID: "queued", Status: state.StatusQueued}
	mate := state.Agent{ID: "mate", Teammate: true, Status: state.StatusTeammate, Name: "reviewer"}

	cases := []struct {
		name  string
		agent state.Agent
		want  string // exact expected right-hand column
	}{
		{"producing shows the rate", producer("live", now, 6000), "0.1k/s"},
		{"open call shows the call timer", openCall, "◐ in Bash 3m00s"},
		{"stuck shows the silence timer", stuck, "2m50s"},
		{"ask shows the silence timer", ask, "52s"},
		{"returned shows neither", done, ""},
		{"queued shows neither", queued, ""},
		{"teammate shows neither", mate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := segsPlain(rowRight(state.World{}, c.agent, v, now, pal))
			if got != c.want {
				t.Errorf("right column = %q, want %q", got, c.want)
			}
		})
	}
}

// §13.4: no stalled row ever displays 0.0k/s. The rate is the mute answer on a
// row that has stopped — the number worth having is how long it has been
// stopped — and `t` must not be able to put it back.
func TestStalledRowNeverShowsZeroRate(t *testing.T) {
	now := colNow()
	pal := NewPalette(2, false)

	silent := producer("silent", now, 0) // no tokens in the window at all
	silent.LastEventAt = now.Add(-3 * time.Minute)

	stuck := silent
	stuck.ID, stuck.Status = "stuck", state.StatusStuck

	ask := silent
	ask.ID, ask.Status = "ask", state.StatusAsk
	ask.Ask = &event.AskPayload{Kind: "command", Command: "rm -rf .cache"}

	unknown := silent
	unknown.ID, unknown.Status = "unknown", state.StatusUnknown

	// A trickle that ROUNDS to nothing is just as mute as nothing: 30 tokens in
	// the window renders 0.0k/s, which is the reading §13.4 forbids.
	trickle := producer("trickle", now, 30)
	trickle.LastEventAt = now.Add(-3 * time.Minute)

	for _, col := range []string{"rate", "since"} {
		v := newUIState(testConfig())
		v.RightCol = col
		for _, a := range []state.Agent{stuck, ask, unknown, silent, trickle} {
			got := segsPlain(rowRight(state.World{}, a, v, now, pal))
			if strings.Contains(got, "0.0k/s") {
				t.Errorf("col=%s %s: right column reads %q — a stalled row must "+
					"never claim a rate of nothing", col, a.ID, got)
			}
			if !strings.Contains(got, "3m00s") {
				t.Errorf("col=%s %s: right column = %q, want the silence timer",
					col, a.ID, got)
			}
		}
	}
}

// `t` still swaps rate and clock for a producing agent (§5.1, §3.18); the
// stuck/ask override is not part of that toggle, which the test above pins.
func TestRateClockToggleAppliesToProducingRows(t *testing.T) {
	now := colNow()
	pal := NewPalette(2, false)
	a := producer("live", now, 6000)
	a.LastEventAt = now.Add(-4 * time.Second)

	v := newUIState(testConfig())
	rate := segsPlain(rowRight(state.World{}, a, v, now, pal))
	if !strings.Contains(rate, "k/s") {
		t.Errorf("default column = %q, want the burn rate", rate)
	}
	v.RightCol = "since"
	clock := segsPlain(rowRight(state.World{}, a, v, now, pal))
	if !strings.Contains(clock, "4s") || strings.Contains(clock, "k/s") {
		t.Errorf("after t the column = %q, want the liveness clock", clock)
	}
}

// A settled row loses the right column, so the tail carries how long ago it
// landed — and it says "returned" at every age (§13.3 Q4), decayed or not.
func TestReturnedTailCarriesTheClock(t *testing.T) {
	now := colNow()
	a := producer("done", now, 1000)
	a.Status, a.DoneAt = state.StatusDone, now.Add(-46*time.Second)
	if got := rowTail(a, now, false); got != "returned · 0:46" {
		t.Errorf("tail = %q, want %q", got, "returned · 0:46")
	}
	a.Decayed = true
	if got := rowTail(a, now, false); got != "returned · 0:46" {
		t.Errorf("decayed tail = %q, want the same wording", got)
	}
	a.DoneAt = time.Time{}
	if got := rowTail(a, now, false); got != "returned" {
		t.Errorf("tail with no return time = %q, want a bare %q", got, "returned")
	}
}
