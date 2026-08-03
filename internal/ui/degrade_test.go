package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/state"
)

// degradeAgent is one wide row with something to lose at every rung of the
// §13.3 Q3 ladder: a long slug, a verified check, a station short of return,
// tokens, and a call carrying a meta suffix.
func degradeAgent(now time.Time) state.Agent {
	a := state.Agent{
		ID:           "a1",
		Name:         "Run the auth test suite in watch mode",
		Status:       state.StatusRun,
		Station:      2,
		Verify:       state.VerifyOK,
		SpawnIndex:   1,
		SpawnedAt:    now.Add(-3 * time.Minute),
		LastEventAt:  now,
		Tokens:       38000,
		ActivityLine: "Run the auth suite",
		CallHistory: []state.Call{
			{Tool: "Bash", Target: "pnpm test --filter auth --run", Meta: "42 passed"},
		},
		TotalCalls: 3,
	}
	a.SparkBuckets[59] = state.SparkBucket{Tokens: 6000, Calls: 1}
	return a
}

// rowLines renders one expanded agent block at width and returns its plain
// lines (branch line dropped).
func rowLines(t *testing.T, a state.Agent, width int, wide bool, now time.Time) []string {
	t.Helper()
	w := state.World{Agents: []state.Agent{a}, Connected: true}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	b := agentBlock(w, v, a, true, true, 6000, 2, false, width, wide, now, NewPalette(2, false))
	out := make([]string, 0, len(b.lines))
	for _, ln := range b.lines {
		out = append(out, segsPlain(ln))
	}
	return out[1:] // the branch glyph line carries nothing under test
}

// TestNarrowDegradationLadder walks the §13.3 Q3 order. At 64 the row is
// complete; at 44 the station label and the meta suffixes are gone, the gauge is
// still there, and both the slug and the right-hand number are intact and
// untruncated at either width.
func TestNarrowDegradationLadder(t *testing.T) {
	now := colNow()
	a := degradeAgent(now)
	slugText := "run-auth-tests"

	wide64 := strings.Join(rowLines(t, a, 64, true, now), "\n")
	for _, want := range []string{slugText, "▇", "first edit", "✓ verified", "42 passed", "38k"} {
		if !strings.Contains(wide64, want) {
			t.Errorf("64 columns dropped %q:\n%s", want, wide64)
		}
	}

	narrow44 := strings.Join(rowLines(t, a, 44, false, now), "\n")
	// 1. the station label goes first; the pips survive, they carry the meaning.
	if strings.Contains(narrow44, "first edit") {
		t.Errorf("44 columns kept the station label:\n%s", narrow44)
	}
	if !strings.Contains(narrow44, pips(a.Station, false)) {
		t.Errorf("44 columns dropped the station pips:\n%s", narrow44)
	}
	// 2. the tool-call meta suffix goes next.
	if strings.Contains(narrow44, "42 passed") {
		t.Errorf("44 columns kept a call meta suffix:\n%s", narrow44)
	}
	// 3. the gauge narrows, it is never absent (§13.4).
	if !strings.Contains(narrow44, gaugeFilled) {
		t.Errorf("44 columns lost the gauge — it may narrow, never vanish:\n%s", narrow44)
	}
	// 4. the inferred marker is the last thing to go, so it is still here.
	if !strings.Contains(narrow44, "✓ verified") {
		t.Errorf("44 columns dropped the verify marker before everything above it:\n%s", narrow44)
	}
	// Never: the slug and the number.
	for _, width := range []int{44, 64} {
		lines := rowLines(t, a, width, width >= 60, now)
		head := lines[0]
		if !strings.Contains(head, slugText) {
			t.Errorf("%d columns truncated the slug: %q", width, head)
		}
		if !strings.Contains(head, "k/s") {
			t.Errorf("%d columns dropped the right-hand number: %q", width, head)
		}
		for i, ln := range lines {
			if got := runewidth.StringWidth(ln); got > width {
				t.Errorf("%d columns: line %d is %d wide: %q", width, i, got, ln)
			}
		}
	}
}

// The ladder is driven by fit, not only by width mode: a row too full for 64
// columns gives up the label first and the verify marker last, in that order.
func TestDegradationOrderIsLabelThenMarker(t *testing.T) {
	now := colNow()
	a := degradeAgent(now)
	// Long enough that the label no longer fits, short enough that the marker
	// still does: exactly the rung the ladder is being tested on.
	a.ActivityLine = "Run the auth suite in watch mode"

	got := strings.Join(rowLines(t, a, 64, true, now), "\n")
	if strings.Contains(got, "first edit") {
		t.Errorf("an overfull row kept the station label:\n%s", got)
	}
	if !strings.Contains(got, "✓ verified") {
		t.Errorf("an overfull row dropped the marker before the label:\n%s", got)
	}

	// Longer still: now the marker goes too, and only then is the activity cut.
	a.ActivityLine = strings.Repeat("keep the auth suite hot in watch mode ", 2)
	got = strings.Join(rowLines(t, a, 64, true, now), "\n")
	if strings.Contains(got, "✓ verified") {
		t.Errorf("the marker survived a row with no room for anything:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("the activity line was not truncated after everything else went:\n%s", got)
	}
}

// A row whose line 1 cannot hold the full bar narrows the gauge instead of
// eating the slug or the number (§13.3 Q3 step 3).
func TestGaugeNarrowsBeforeTheSlugIsTouched(t *testing.T) {
	now := colNow()
	a := degradeAgent(now)
	a.Retries = 2
	head := rowLines(t, a, 40, false, now)[0]
	if !strings.Contains(head, "run-auth-tests") {
		t.Errorf("the slug was cut instead of the gauge: %q", head)
	}
	if !strings.Contains(head, "k/s") {
		t.Errorf("the number was cut instead of the gauge: %q", head)
	}
	if n := strings.Count(head, gaugeFilled) + strings.Count(head, gaugeEmpty); n > gaugeCells {
		t.Errorf("the gauge did not narrow: %q", head)
	}
	if !strings.Contains(head, gaugeFilled) {
		t.Errorf("the gauge vanished at 40 columns: %q", head)
	}
}
