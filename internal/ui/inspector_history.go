package ui

import (
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// PART 13 Q1 — activity history lives in the INSPECTOR, never back on the row.
//
// The ruling: "one bar per 10s bucket across the whole agent lifetime, on the
// same cross-agent scale as the row, so the inspector and the row can be read
// against each other. Nothing on the row gets narrower to make space." This
// file is that bar row and nothing else touches the row's geometry.
//
// Two constraints shape it, and both are data facts rather than taste:
//
//   - Glyphs. §13.5/Q5 leaves the gauge exactly two marks, ▇ and ·; ▁▂▃▄▅▆ were
//     withdrawn with the sparkline. A history cell therefore has two states, not
//     seven, so a bucket says "this agent was working at a level the row would
//     have shown" or "it was not". Height is gone; occupancy over time is what
//     is left, and it is the honest thing to draw with two glyphs.
//   - Horizon. state.Agent keeps a strictly rolling 60 one-second SparkBuckets
//     ending at the snapshot instant (state.agentState.sparkSnapshot rebuilds the
//     window from `now` on every frame), so at 10s granularity SIX buckets is the
//     entire history that exists anywhere in the process. There is no lifetime
//     series to draw. C8 forbids inventing one, so this renders the retained
//     window and says out loud that it is a window: an agent alive for four
//     minutes is labelled "last 60s of 4:12", never presented as its run.
//     See historyNeeds for the state.Agent field that would fix this.
const (
	// historyBucketSeconds is the §13.3 Q1 granularity: one bar per 10s.
	historyBucketSeconds = 10
	// historyCells is how many 10s buckets the retained window holds. It is a
	// data ceiling, not a layout choice: 60 one-second buckets / 10.
	historyCells = 60 / historyBucketSeconds
	// sparkWindowSeconds is the span state.Agent.SparkBuckets covers.
	sparkWindowSeconds = 60

	// historyLabel prefixes the row. Kept short because the bars and the scale
	// note are the content.
	historyLabel = "history "
)

// historySeries is one agent's drawable history plus everything the view has to
// be honest about. Levels is oldest-first and holds only buckets that the agent
// was actually alive for — a pre-spawn bucket is not a quiet bucket, and padding
// the left of the chart with · would fabricate silence the agent never had.
type historySeries struct {
	Levels []int
	// Metric is the metric Levels are counted in, after the same tokens→calls
	// fallback the row applies (effectiveSpark), so the bars and the row can
	// never be measured in different units.
	Metric string
	// Lifetime is spawn→now (or spawn→done for a settled agent).
	Lifetime time.Duration
	// Clipped reports that Lifetime exceeds the retained window, i.e. the bars
	// are a tail of the run rather than the run.
	Clipped bool
	// Recorded reports whether the agent has any activity anywhere in the
	// model — tokens or calls, at any time, not just inside the window. False
	// means "nothing was ever recorded", which is a different statement from
	// "the last minute was quiet" and is said differently.
	Recorded bool
}

// historyFor builds the series for one agent under the metric the row is using.
func historyFor(a state.Agent, metric string, now time.Time) historySeries {
	end := now
	if a.Status == state.StatusDone && !a.DoneAt.IsZero() {
		end = a.DoneAt
	}
	out := historySeries{
		Recorded: a.Tokens > 0 || a.TotalCalls > 0,
	}
	if !a.SpawnedAt.IsZero() && end.After(a.SpawnedAt) {
		out.Lifetime = end.Sub(a.SpawnedAt)
	}
	out.Clipped = out.Lifetime > sparkWindowSeconds*time.Second

	// effectiveSpark, not a local fallback: the row decides tokens-vs-calls
	// from the same 60 buckets, and two independent implementations of that
	// decision is exactly how the inspector and the row would end up measuring
	// different things.
	eff, _ := effectiveSpark(a.SparkBuckets, metric)
	out.Metric = eff
	if a.SparkAt.IsZero() {
		return out // no window at all: the machine has never sampled this agent
	}

	sums := bucket10(a.SparkBuckets, eff)
	first := 0
	if !a.SpawnedAt.IsZero() {
		for first < historyCells && historyBucketEnd(a.SparkAt, first).Before(a.SpawnedAt) {
			first++
		}
	}
	if first >= historyCells {
		return out
	}
	out.Levels = sums[first:]
	return out
}

// historyBucketEnd is the wall-clock instant bucket i ends at. Bucket index 59
// of SparkBuckets is the SparkAt second (state.agentState.sparkSnapshot), so
// bucket i covers spark indices [10i, 10i+9] and ends 50-10i seconds before
// SparkAt.
func historyBucketEnd(sparkAt time.Time, i int) time.Time {
	return sparkAt.Add(-time.Duration(sparkWindowSeconds-historyBucketSeconds-i*historyBucketSeconds) * time.Second)
}

// bucket10 folds the 60 one-second buckets into historyCells 10s sums.
func bucket10(buckets [60]state.SparkBucket, metric string) []int {
	out := make([]int, historyCells)
	for i := range out {
		for j := i * historyBucketSeconds; j < (i+1)*historyBucketSeconds; j++ {
			if metric == "calls" {
				out[i] += buckets[j].Calls
			} else {
				out[i] += buckets[j].Tokens
			}
		}
	}
	return out
}

// historyShareCells is how many of the ROW's gauge cells this bucket's work
// would have lit, and it is the whole of the shared-scale claim.
//
// The row divides an agent's per-bucket level by runMax — the busiest live
// agent's level over the row's own ~8.57s buckets — and rounds to gaugeCells. A
// 10s bucket holds more seconds than an 8.57s one, so comparing the two counts
// directly would inflate every history bar by 60/7/10 ≈ 1.17× and the inspector
// would flatter the agent against its own row. Both sides are converted to a
// per-second rate first; after that the arithmetic and the rounding are
// activityGauge's verbatim, and its one-cell floor for a MOVING agent maps onto
// "this bucket holds work" — which is what moving means once the instant has
// passed. TestHistorySharesRowScale asserts the two agree cell-for-cell against
// the real activityGauge, so a change to gauge.go's scaling, rounding or floor
// fails a test instead of quietly desynchronising the two views.
func historyShareCells(level, runMax int) int {
	if runMax <= 0 || level <= 0 {
		return 0
	}
	rowUnit := float64(sparkWindowSeconds) / float64(sparkCells) // seconds per row bucket
	share := (float64(level) / float64(historyBucketSeconds)) /
		(float64(runMax) / rowUnit)
	if share > 1 {
		share = 1
	}
	cells := int(share*float64(gaugeCells) + 0.5)
	if cells == 0 {
		cells = 1 // activityGauge's C8 floor: work that happened never reads as none
	}
	if cells > gaugeCells {
		cells = gaugeCells
	}
	return cells
}

// historyFilled decides one bucket's glyph. With ▇ and · the only marks §13.5
// leaves, a cell can say "the row would have shown this" or nothing; the
// magnitude that a taller bar used to carry moves into the note, which reports
// the peak bucket in the row's own cells (historyPeak).
func historyFilled(level, runMax int) bool {
	return historyShareCells(level, runMax) >= 1
}

// historyPeak is the busiest bucket expressed in row cells, so a reader can put
// the inspector next to the row and get the same number.
func historyPeak(s historySeries, runMax int) int {
	peak := 0
	for _, lvl := range s.Levels {
		if c := historyShareCells(lvl, runMax); c > peak {
			peak = c
		}
	}
	return peak
}

// historyBars renders the ▇/· run for a series against the run-wide scale.
func historyBars(s historySeries, runMax int) string {
	var b strings.Builder
	for _, lvl := range s.Levels {
		if historyFilled(lvl, runMax) {
			b.WriteString(gaugeFilled)
		} else {
			b.WriteString(gaugeEmpty)
		}
	}
	return b.String()
}

// historyNote states the granularity, the shared scale and the horizon, in that
// order and in the fewest words that stay true.
func historyNote(s historySeries, runMax int) string {
	note := itoa(historyBucketSeconds) + "s/bar"
	if peak := historyPeak(s, runMax); peak > 0 {
		note += " · peak " + itoa(peak) + "/" + itoa(gaugeCells) + " of run"
	}
	switch {
	case s.Clipped:
		note += " · last " + itoa(sparkWindowSeconds) + "s of " + formatClock(s.Lifetime)
	case s.Lifetime > 0:
		note += " · whole run (" + formatClock(s.Lifetime) + ")"
	}
	return note
}

// historyAbsence is the honest-absence copy: what to say when there is no chart
// to draw. C8 — an empty bar row would read as "measured, and it was zero".
func historyAbsence(s historySeries, kind string) string {
	switch kind {
	case "main":
		// state.Main has no SparkBuckets at all; nothing is sampled per second
		// for the root agent, so there is nothing to bucket.
		return "no per-second history for the root agent"
	case "teammate":
		return "no history — limited telemetry"
	}
	if !s.Recorded {
		return "no activity recorded"
	}
	return "no history retained"
}

// inspectorHistoryRow is the line the inspector draws: the label, the bars, and
// the scale/horizon note — or a stated absence, never an empty chart.
func inspectorHistoryRow(w state.World, v *UIState, width int, now time.Time, pal Palette) []seg {
	if v.SelID == event.MainAgentID || v.SelID == selBand {
		return truncSegs(line(
			seg{historyLabel, pal.Deep},
			seg{historyAbsence(historySeries{}, "main"), pal.Settled},
		), width)
	}
	a, ok := agentByID(w, v.SelID)
	if !ok {
		return nil
	}
	if a.Teammate {
		return truncSegs(line(
			seg{historyLabel, pal.Deep},
			seg{historyAbsence(historySeries{}, "teammate"), pal.Settled},
		), width)
	}
	s := historyFor(a, v.SparkMetric, now)
	// !Recorded is the §13.1 amendment restated as a drawing rule: an agent
	// with no tool call and no tokens ANYWHERE in the model has nothing to
	// chart, and six · would claim a measured flatline. An agent whose work is
	// simply older than the window keeps its bars — the zeros there are real
	// measurements, and the note says how much of the run they cover.
	if len(s.Levels) == 0 || !s.Recorded {
		return truncSegs(line(
			seg{historyLabel, pal.Deep},
			seg{historyAbsence(s, ""), pal.Settled},
		), width)
	}
	runMax := runActivityMax(v.orderedAgents(w), v.SparkMetric)
	bars := historyBars(s, runMax)
	// The note is the first thing to go when the pane is narrow: the bars and
	// their granularity are the content, and truncSegs marks the cut with …
	// rather than dropping it silently.
	return truncSegs(line(
		seg{historyLabel, pal.Deep},
		seg{bars, pal.Live},
		seg{"  " + historyNote(s, runMax), pal.Deep},
	), width)
}

// What state.Agent would need for a TRUE lifetime chart instead of this 60s
// tail, for whoever owns internal/state next:
//
//	// HistoryBuckets are 10s activity buckets since spawn, oldest first,
//	// counting the same tokens and calls as SparkBuckets. Unlike SparkBuckets
//	// they never roll: the slice grows for the agent's whole life (360 entries
//	// per hour, two ints each — a rounding error next to CallHistory).
//	HistoryBuckets []SparkBucket
//	HistoryFrom    time.Time // the instant HistoryBuckets[0] starts at
//
// With those two fields historyFor drops its SparkBuckets path, Clipped becomes
// permanently false, and nothing else in this file changes.
