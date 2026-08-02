package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The activity gauge replaces the §3.4 sparkline on the row.
//
// The sparkline drew seven bars of the trailing 60s, each agent scaled against
// its OWN maximum. Two agents showing an identical shape could differ by three
// orders of magnitude, the shape rescaled whenever a tall bucket rolled out of
// the window, and at seven cells a burst and a lull look much alike. It was
// unreadable in practice — the one element on the row that carried no
// transferable meaning.
//
// What replaces it answers the two questions a glance actually asks:
//
//	is this agent moving?  — a braille spinner, stepped on the 1s tick, shown
//	                         ONLY while the agent is producing. A still row is
//	                         genuinely still; motion is never decorative.
//	how hard, next to the  — a filled bar on a SHARED scale: every agent is
//	others?                  measured against the busiest agent in the run, so
//	                         a longer bar always means more work. Comparing
//	                         rows is the whole point of a tree.
//
// The history is not lost: the inspector still holds the full call log, and
// the numbers (tokens, rate) remain exact on the row.
const (
	gaugeCells = 5

	// gaugeFilled/gaugeEmpty are §3.11 glyphs. ▇ reads as a solid block at
	// this size while leaving ▁..▆ free, and · is the established "nothing
	// here" mark (the flatline).
	gaugeFilled = "▇"
	gaugeEmpty  = "·"

	// gaugeIdle is how long an agent may be silent before the spinner stops.
	// Slightly longer than the 1s tick so a steady worker does not stutter
	// between events.
	gaugeIdle = 3 * time.Second
)

// activityGauge renders one agent's gauge: an optional motion glyph followed
// by gaugeCells of bar. share is the agent's activity as a fraction of the
// busiest agent in the run (0..1); moving reports whether it has produced
// anything recently.
//
// A moving agent always shows at least one filled cell — it is doing
// something, and rounding that to nothing would be a lie in the direction that
// matters (C8). A still agent with no work shows the empty bar, which is the
// flatline's meaning: nothing is running.
func activityGauge(share float64, moving bool, now time.Time) string {
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
	filled := int(share*float64(gaugeCells) + 0.5)
	if filled == 0 && moving {
		filled = 1
	}
	if filled > gaugeCells {
		filled = gaugeCells
	}
	return head + strings.Repeat(gaugeFilled, filled) +
		strings.Repeat(gaugeEmpty, gaugeCells-filled)
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

// gaugeFor renders the gauge for one agent against the run-wide scale.
func gaugeFor(a state.Agent, metric string, runMax int, now time.Time) string {
	level := agentActivity(a, metric)
	moving := !a.LastEventAt.IsZero() && now.Sub(a.LastEventAt) <= gaugeIdle &&
		a.Status != state.StatusDone
	share := 0.0
	if runMax > 0 {
		share = float64(level) / float64(runMax)
	}
	return activityGauge(share, moving, now)
}
