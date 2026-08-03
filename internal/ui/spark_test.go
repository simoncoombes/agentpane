package ui

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The sparkline renderer and `▁▂▃▄▅▆` are withdrawn (§13.3 Q5), so what is left
// to test is the arithmetic the gauge, the burn rate and the inspector's
// history all share.

func TestEffectiveSparkFallsBackToCalls(t *testing.T) {
	var b [60]state.SparkBucket
	b[10].Calls = 2
	b[55].Calls = 4

	// A token-less agent still registers its tool calls (§3.4 fallback), and
	// the caller is told which metric it is now looking at — a token-sized
	// scale must never be applied to a call count.
	metric, max := effectiveSpark(b, "tokens")
	if metric != "calls" || max != 4 {
		t.Errorf("effectiveSpark = (%q, %d), want (calls, 4)", metric, max)
	}

	// With token data present, tokens win and calls are ignored.
	b[20].Tokens = 100
	if metric, max = effectiveSpark(b, "tokens"); metric != "tokens" || max != 100 {
		t.Errorf("effectiveSpark = (%q, %d), want (tokens, 100)", metric, max)
	}

	// Nothing at all: no metric flip, no invented level.
	var empty [60]state.SparkBucket
	if metric, max = effectiveSpark(empty, "tokens"); metric != "tokens" || max != 0 {
		t.Errorf("effectiveSpark on an empty window = (%q, %d), want (tokens, 0)", metric, max)
	}
}

func TestBucketizeCoversTheWholeWindow(t *testing.T) {
	var b [60]state.SparkBucket
	for i := range b {
		b[i].Tokens = 1
	}
	vals := bucketize(b, "tokens")
	sum := 0
	for _, v := range vals {
		sum += v
	}
	if sum != 60 {
		t.Errorf("bucketize dropped seconds: sum=%d, want 60", sum)
	}
	if allZero(vals) {
		t.Error("allZero on a full window")
	}
}

func TestTokensLastMinute(t *testing.T) {
	var b [60]state.SparkBucket
	b[0].Tokens = 100
	b[59].Tokens = 20
	if got := tokensLastMinute(b); got != 120 {
		t.Errorf("tokensLastMinute = %d, want 120", got)
	}
}

// §13.3 Q5 over a whole frame: no render path may put a withdrawn glyph on
// screen, at any width, with or without the inspector.
func TestNoWithdrawnGlyphsInAFrame(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	for _, cols := range []int{38, 44, 64, 100} {
		for _, insp := range []bool{false, true} {
			v.InspectorOpen = insp
			frame, _ := renderFrame(w, v, cols, 44, demoNow(), NewPalette(2, false))
			if i := strings.IndexAny(frame, "▁▂▃▄▅▆"); i >= 0 {
				t.Errorf("cols=%d inspector=%v: withdrawn glyph %q in the frame:\n%s",
					cols, insp, string([]rune(frame[i:])[0]), stripANSI(frame))
			}
		}
	}
}

// §13.3 Q5: the withdrawn glyphs must not come back through the gauge.
func TestGaugeDrawsNoWithdrawnGlyphs(t *testing.T) {
	now := demoNow()
	var b [60]state.SparkBucket
	b[59].Tokens = 500
	a := state.Agent{ID: "a", Status: state.StatusRun, LastEventAt: now, SparkBuckets: b}
	for _, cells := range []int{gaugeCells, gaugeCellsNarrow} {
		got := gaugeFor(a, "tokens", 500, cells, now)
		if strings.ContainsAny(got, "▁▂▃▄▅▆") {
			t.Errorf("gauge drew a withdrawn glyph: %q", got)
		}
	}
}
