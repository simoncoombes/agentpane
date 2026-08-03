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
// Three constraints shape it, and all three are data facts rather than taste:
//
//   - Glyphs. §13.5/Q5 leaves the gauge exactly two marks, ▇ and ·; ▁▂▃▄▅▆ were
//     withdrawn with the sparkline. A history cell therefore has two states, not
//     seven, so a bucket says "this agent was working at a level the row would
//     have shown" or "it was not". Height is gone; occupancy over time is what
//     is left, and it is the honest thing to draw with two glyphs.
//   - Horizon. state.Agent.HistoryBuckets is the LIFETIME series: 10s buckets
//     spanning spawn→now, bounded at state's histCap and halving its own
//     resolution when it fills, so the span always covers the run. This file
//     reads the bucket width off Agent.HistoryStep and never assumes it — a
//     chart labelled 10s/bar that is really 40s/bar is the same quiet lie about
//     the horizon that the old rolling window told (C8). Before the lifetime
//     series existed the only history in the process was the trailing 60s
//     SparkBuckets window; that path is still here as a fallback for a world
//     that carries no series, and it still says out loud that it is a tail.
//   - Width. One bar per 10s of a twenty-minute agent is 120 bars, which no
//     pane has room for. The bars are folded to fit (historyDrawCells) by
//     grouping whole source buckets, and the note reports the width a bar
//     actually ends up covering. Folding is the only honest way to lose
//     resolution; clipping would lose the span, which is the thing the ruling
//     asks for.
const (
	// historyBucketSeconds is the §13.3 Q1 granularity: one bar per 10s. It is
	// what the fallback path buckets at and what the lifetime series starts at.
	historyBucketSeconds = 10
	// historyCells is how many 10s buckets the FALLBACK (60s SparkBuckets)
	// window holds. A data ceiling, not a layout choice: 60 / 10.
	historyCells = 60 / historyBucketSeconds
	// sparkWindowSeconds is the span state.Agent.SparkBuckets covers.
	sparkWindowSeconds = 60

	// historyDrawCells is the most bars the row will draw. It is a width budget,
	// not a taste call: 8 bars leaves the label, the two-space gap and the whole
	// note inside a 64-column pane with nothing truncated, and keeps the bars
	// intact at 44 (where only the note is cut).
	historyDrawCells = 8

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
	// BarSeconds is how many seconds ONE entry of Levels covers. It is not a
	// constant: the lifetime series coarsens as a run grows, and folding to fit
	// the pane coarsens it again. Every piece of arithmetic and every word of
	// copy below reads this rather than assuming historyBucketSeconds.
	BarSeconds int
	// Metric is the metric Levels are counted in, after the same tokens→calls
	// fallback the row applies (effectiveSpark), so the bars and the row can
	// never be measured in different units.
	Metric string
	// Lifetime is spawn→now (or spawn→done for a settled agent).
	Lifetime time.Duration
	// Clipped reports that the bars do not reach back to the start of Lifetime,
	// i.e. they are a tail of the run rather than the run. With the lifetime
	// series present this is false; it is what the SparkBuckets fallback has to
	// admit to.
	Clipped bool
	// Recorded reports whether the agent has any activity anywhere in the
	// model — tokens or calls, at any time, not just inside the window. False
	// means "nothing was ever recorded", which is a different statement from
	// "the last minute was quiet" and is said differently.
	Recorded bool
}

// Covered is the span the bars actually represent.
func (s historySeries) Covered() time.Duration {
	return time.Duration(len(s.Levels)*s.BarSeconds) * time.Second
}

// historyFor builds the series for one agent under the metric the row is using.
// The result is at the finest resolution available; fold() reduces it to what the
// pane can draw.
func historyFor(a state.Agent, metric string, now time.Time) historySeries {
	end := now
	if a.Status == state.StatusDone && !a.DoneAt.IsZero() {
		end = a.DoneAt
	}
	out := historySeries{
		BarSeconds: historyBucketSeconds,
		Recorded:   a.Tokens > 0 || a.TotalCalls > 0,
	}
	if !a.SpawnedAt.IsZero() && end.After(a.SpawnedAt) {
		out.Lifetime = end.Sub(a.SpawnedAt)
	}

	// effectiveSpark, not a local fallback: the row decides tokens-vs-calls from
	// the same 60 buckets, and two independent implementations of that decision
	// is exactly how the inspector and the row would end up measuring different
	// things.
	eff, _ := effectiveSpark(a.SparkBuckets, metric)
	out.Metric = eff

	if len(a.HistoryBuckets) > 0 && a.HistoryStep > 0 {
		// §3.4's fallback applied to the data being DRAWN. The row's decision is
		// made from the trailing minute, which for a settled or long-quiet agent
		// can be empty in the metric that carried its whole life; drawing an
		// all-empty chart over work that happened would be the C8 failure the
		// absence copy exists to avoid. The switch can only fire where the row is
		// showing nothing either.
		if sumSeries(a.HistoryBuckets, eff) == 0 {
			if other := otherMetric(eff); sumSeries(a.HistoryBuckets, other) > 0 {
				eff, out.Metric = other, other
			}
		}
		out.BarSeconds = int(a.HistoryStep / time.Second)
		if out.BarSeconds < 1 {
			out.BarSeconds = 1
		}
		out.Levels = levelsOf(a.HistoryBuckets, eff)
		// The series starts at the spawn bucket, so it normally covers the whole
		// lifetime. It can still fall short — an agent whose spawn instant was
		// learned after its first activity — and when it does the note says so
		// rather than implying the bars are the run.
		out.Clipped = out.Lifetime > out.Covered()+time.Duration(out.BarSeconds)*time.Second
		return out
	}

	// Fallback: the rolling 60s window, which is a TAIL of anything longer.
	out.Clipped = out.Lifetime > sparkWindowSeconds*time.Second
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

// fold groups the series into at most cells bars. Whole source buckets are
// grouped, so a bar always spans an exact multiple of the source width and the
// reported width is exact rather than nominal.
//
// The last bar may cover fewer source buckets than the rest when the count does
// not divide; its per-second rate is then computed against the full bar width and
// so reads slightly low. Understating work is the safe direction here — the
// alternative is a bar that claims a rate its window did not sustain.
func (s historySeries) fold(cells int) historySeries {
	if cells < 1 || len(s.Levels) <= cells {
		return s
	}
	group := (len(s.Levels) + cells - 1) / cells
	out := make([]int, 0, (len(s.Levels)+group-1)/group)
	for i := 0; i < len(s.Levels); i += group {
		sum := 0
		for j := i; j < i+group && j < len(s.Levels); j++ {
			sum += s.Levels[j]
		}
		out = append(out, sum)
	}
	s.Levels = out
	s.BarSeconds *= group
	return s
}

func otherMetric(m string) string {
	if m == "calls" {
		return "tokens"
	}
	return "calls"
}

func sumSeries(buckets []state.SparkBucket, metric string) int {
	n := 0
	for _, b := range buckets {
		if metric == "calls" {
			n += b.Calls
		} else {
			n += b.Tokens
		}
	}
	return n
}

func levelsOf(buckets []state.SparkBucket, metric string) []int {
	out := make([]int, len(buckets))
	for i, b := range buckets {
		if metric == "calls" {
			out[i] = b.Calls
		} else {
			out[i] = b.Tokens
		}
	}
	return out
}

// historyBucketEnd is the wall-clock instant fallback bucket i ends at. Bucket
// index 59 of SparkBuckets is the SparkAt second (state.agentState.sparkSnapshot),
// so bucket i covers spark indices [10i, 10i+9] and ends 50-10i seconds before
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

// historyShareCells is how many of the ROW's gauge cells this bar's work would
// have lit, and it is the whole of the shared-scale claim.
//
// The row divides an agent's per-bucket level by runMax — the busiest live
// agent's level over the row's own ~8.57s buckets — and rounds to gaugeCells. A
// history bar covers a different number of seconds, so comparing the two counts
// directly would flatter or penalise the agent against its own row. Both sides
// are converted to a per-second rate first; after that the arithmetic and the
// rounding are activityGauge's verbatim, and its one-cell floor for a MOVING
// agent maps onto "this bar holds work" — which is what moving means once the
// instant has passed. TestHistorySharesRowScale asserts the two agree
// cell-for-cell against the real activityGauge, so a change to gauge.go's
// scaling, rounding or floor fails a test instead of quietly desynchronising the
// two views.
//
// barSeconds is a parameter and not a constant because the lifetime series
// coarsens: the rate conversion is only exact if it uses the width the bar
// actually covers.
func historyShareCells(level, runMax, barSeconds int) int {
	if runMax <= 0 || level <= 0 || barSeconds <= 0 {
		return 0
	}
	rowUnit := float64(sparkWindowSeconds) / float64(sparkCells) // seconds per row bucket
	share := (float64(level) / float64(barSeconds)) /
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

// historyFilled decides one bar's glyph. With ▇ and · the only marks §13.5
// leaves, a cell can say "the row would have shown this" or nothing; the
// magnitude that a taller bar used to carry moves into the note, which reports
// the peak bar in the row's own cells (historyPeak).
func historyFilled(level, runMax, barSeconds int) bool {
	return historyShareCells(level, runMax, barSeconds) >= 1
}

// historyPeak is the busiest bar expressed in row cells, so a reader can put the
// inspector next to the row and get the same number.
func historyPeak(s historySeries, runMax int) int {
	peak := 0
	for _, lvl := range s.Levels {
		if c := historyShareCells(lvl, runMax, s.BarSeconds); c > peak {
			peak = c
		}
	}
	return peak
}

// historyBars renders the ▇/· run for a series against the run-wide scale.
func historyBars(s historySeries, runMax int) string {
	var b strings.Builder
	for _, lvl := range s.Levels {
		if historyFilled(lvl, runMax, s.BarSeconds) {
			b.WriteString(gaugeFilled)
		} else {
			b.WriteString(gaugeEmpty)
		}
	}
	return b.String()
}

// historyNote states the granularity, the shared scale and the horizon, in that
// order and in the fewest words that stay true. The granularity is read off the
// series, never assumed: it is the one number that tells the reader how much
// averaging is between them and the events.
func historyNote(s historySeries, runMax int) string {
	note := itoa(s.BarSeconds) + "s/bar"
	if peak := historyPeak(s, runMax); peak > 0 {
		note += " · peak " + itoa(peak) + "/" + itoa(gaugeCells) + " of run"
	}
	switch {
	case s.Clipped:
		note += " · last " + spanText(s.Covered()) + " of " + formatClock(s.Lifetime)
	case s.Lifetime > 0:
		note += " · whole run (" + formatClock(s.Lifetime) + ")"
	}
	return note
}

// spanText renders a covered span: seconds up to a minute (the tail the
// SparkBuckets fallback shows is naturally read as "60s"), m:ss beyond.
func spanText(d time.Duration) string {
	if sec := int(d / time.Second); sec <= 60 {
		return itoa(sec) + "s"
	}
	return formatClock(d)
}

// historyAbsence is the honest-absence copy: what to say when there is no chart
// to draw. C8 — an empty bar row would read as "measured, and it was zero".
func historyAbsence(s historySeries, kind string) string {
	switch kind {
	case "main":
		// state.Main has no SparkBuckets and no lifetime series; nothing is
		// sampled for the root agent, so there is nothing to bucket.
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
	s := historyFor(a, v.SparkMetric, now).fold(historyDrawCells)
	// !Recorded is the §13.1 amendment restated as a drawing rule: an agent
	// with no tool call and no tokens ANYWHERE in the model has nothing to
	// chart, and a row of · would claim a measured flatline. An agent whose work
	// is simply older than the bars keeps them — the zeros there are real
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
