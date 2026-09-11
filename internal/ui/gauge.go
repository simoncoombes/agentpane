package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The five-cell activity scale.
//
// It began as the row's gauge, which replaced the §3.4 sparkline: seven bars
// scaled per agent, where two identical shapes could differ by three orders of
// magnitude. The gauge fixed the scale — every agent measured against the
// busiest in the run — and then earned its own removal: five cells of accent
// block on every live row was the loudest thing in the pane, repeated once per
// agent, and the exact number beside it already said more.
//
// What survives is the scale itself, because the inspector's history bars are
// drawn in it (inspector_history.go) and its note quotes it — "peak 3/5 of run"
// means three of these five cells. activityGaugeN is the reference the
// inspector's own rounding is cross-checked against, so a change here fails a
// test rather than quietly desynchronising the two.
const (
	gaugeCells = 5

	// gaugeFilled/gaugeEmpty are §3.11 glyphs. ▇ reads as a solid block at
	// this size while leaving ▁..▆ free, and · is the established "nothing
	// here" mark (the flatline).
	gaugeFilled = "▇"
	gaugeEmpty  = "·"
)

// activityGauge renders one agent's gauge at the full row width. Callers with
// a narrow row use activityGaugeN; this signature is the one the inspector's
// history shares its rounding with, so it stays the plain form.
func activityGauge(share float64, moving bool, now time.Time) string {
	return activityGaugeN(share, moving, gaugeCells, now)
}

// activityGaugeN renders one agent's gauge: an optional motion glyph followed
// by cells of bar. share is the agent's activity as a fraction of the busiest
// agent in the run (0..1); moving reports whether it has produced anything
// recently. cells is gaugeCells normally and gaugeCellsNarrow on a row that
// could not fit the full bar; below one cell it is clamped, never removed —
// the gauge never degrades to nothing (§13.3 Q3).
//
// A moving agent always shows at least one filled cell — it is doing
// something, and rounding that to nothing would be a lie in the direction that
// matters (C8). A still agent with no work shows the empty bar: nothing is
// running.
func activityGaugeN(share float64, moving bool, cells int, now time.Time) string {
	if cells < 1 {
		cells = 1
	}
	head := " "
	if moving {
		head = spinner(now)
	}
	if share < 0 {
		share = 0
	}
	if share > 1 {
		share = 1
	}
	filled := int(share*float64(cells) + 0.5)
	if filled == 0 && moving {
		filled = 1
	}
	if filled > cells {
		filled = cells
	}
	return head + strings.Repeat(gaugeFilled, filled) +
		strings.Repeat(gaugeEmpty, cells-filled)
}

// agentActivity is one agent's current work level in the units the row is
// drawing, using the same tokens→calls fallback as the old sparkline so a
// token-less agent still registers its tool calls.
func agentActivity(a state.Agent, metric string) int {
	_, max := effectiveSpark(a.SparkBuckets, metric)
	return max
}

// runActivityMax is the busiest agent's level, the denominator that makes bars
// comparable. Settled agents are excluded: a finished burst should not shrink
// everything that is still running.
func runActivityMax(agents []state.Agent, metric string) int {
	max := 0
	for _, a := range agents {
		if a.Status == state.StatusDone || a.Teammate {
			continue
		}
		if v := agentActivity(a, metric); v > max {
			max = v
		}
	}
	return max
}
