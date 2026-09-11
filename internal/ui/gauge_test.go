package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

func TestActivityGauge(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 3, 0, time.UTC)

	cases := []struct {
		name   string
		share  float64
		moving bool
		filled int
		spins  bool
	}{
		{"idle and empty", 0, false, 0, false},
		{"busiest agent", 1, true, gaugeCells, true},
		{"half", 0.5, true, 3, true}, // 2.5 rounds up
		{"working but tiny", 0.01, true, 1, true},
		{"settled with history", 0.8, false, 4, false},
		{"clamped above", 5, true, gaugeCells, true},
		{"clamped below", -3, false, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := activityGauge(c.share, c.moving, now)
			if n := strings.Count(got, gaugeFilled); n != c.filled {
				t.Errorf("%q: filled=%d want %d", got, n, c.filled)
			}
			if n := len([]rune(got)); n != gaugeCells+1 {
				t.Errorf("%q: width=%d want %d (motion glyph + bar)", got, n, gaugeCells+1)
			}
			spinning := !strings.HasPrefix(got, " ")
			if spinning != c.spins {
				t.Errorf("%q: spinning=%v want %v", got, spinning, c.spins)
			}
		})
	}
}

// A caller asking for a narrower bar gets one, and a caller asking for nothing
// still gets a cell: the scale never degrades to no mark at all.
func TestGaugeNarrowsButNeverVanishes(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 3, 0, time.UTC)
	for _, c := range []struct{ cells, want int }{
		{gaugeCells, gaugeCells}, {3, 3},
		{1, 1}, {0, 1}, {-4, 1},
	} {
		got := activityGaugeN(1, true, c.cells, now)
		if n := len([]rune(got)) - 1; n != c.want { // less the motion glyph
			t.Errorf("cells=%d: bar is %d wide (%q), want %d", c.cells, n, got, c.want)
		}
		if strings.Count(got, gaugeFilled) != c.want {
			t.Errorf("cells=%d: the busiest agent must fill the bar: %q", c.cells, got)
		}
	}
	// A narrowed bar still ranks: half the work is still visibly less.
	half := activityGaugeN(0.34, true, 3, now)
	full := activityGaugeN(1, true, 3, now)
	if strings.Count(half, gaugeFilled) >= strings.Count(full, gaugeFilled) {
		t.Errorf("narrow gauge stopped ranking: %q vs %q", half, full)
	}
}

// A settled agent must not shrink everyone still working.
func TestSettledAgentsDoNotSetTheScale(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	done := state.Agent{ID: "done", Status: state.StatusDone}
	done.SparkBuckets[59] = state.SparkBucket{Tokens: 100000}
	live := state.Agent{ID: "live", Status: state.StatusRun, LastEventAt: now}
	live.SparkBuckets[59] = state.SparkBucket{Tokens: 50}

	if got := runActivityMax([]state.Agent{done, live}, "tokens"); got != 50 {
		t.Errorf("runMax = %d, want the live agent's level", got)
	}
}
