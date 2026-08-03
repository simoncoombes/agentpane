package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

func histNow() time.Time { return time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC) }

// histAgent builds a live agent whose spark window ends at now.
func histAgent(id string, now time.Time, spawnAgo time.Duration) state.Agent {
	return state.Agent{
		ID:          id,
		Name:        "port the routes",
		Type:        "general-purpose",
		Status:      state.StatusRun,
		SpawnedAt:   now.Add(-spawnAgo),
		LastEventAt: now,
		SparkAt:     now,
	}
}

// The granularity IS the ruling: one bar per 10s bucket, and the number of bars
// is bounded by the data that exists rather than chosen for looks.
func TestHistoryBucketGranularity(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 5*time.Minute)
	// One token in each of the 60 one-second buckets: every 10s bucket has
	// activity, so the whole window is filled.
	for i := range a.SparkBuckets {
		a.SparkBuckets[i] = state.SparkBucket{Tokens: 100}
	}
	a.Tokens = 6000

	s := historyFor(a, "tokens", now)
	if len(s.Levels) != historyCells {
		t.Fatalf("levels = %d, want %d (60s / %ds)", len(s.Levels), historyCells, historyBucketSeconds)
	}
	if historyCells != 6 || historyBucketSeconds != 10 {
		t.Fatalf("granularity drifted: %d buckets of %ds", historyCells, historyBucketSeconds)
	}
	for i, lvl := range s.Levels {
		if lvl != 10*100 {
			t.Errorf("bucket %d = %d, want %d (ten 1s buckets summed)", i, lvl, 10*100)
		}
	}
	if bars := historyBars(s, 100); bars != strings.Repeat(gaugeFilled, historyCells) {
		t.Errorf("bars = %q, want %d filled", bars, historyCells)
	}
}

// Buckets are ordered oldest-first and land in the right slot: activity in the
// last two seconds must light the LAST bar, not the first.
func TestHistoryBucketOrderingIsOldestFirst(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 5*time.Minute)
	a.SparkBuckets[58] = state.SparkBucket{Tokens: 500}
	a.SparkBuckets[59] = state.SparkBucket{Tokens: 500}
	a.Tokens = 1000

	s := historyFor(a, "tokens", now)
	if len(s.Levels) != historyCells {
		t.Fatalf("levels = %d", len(s.Levels))
	}
	for i := 0; i < historyCells-1; i++ {
		if s.Levels[i] != 0 {
			t.Errorf("bucket %d = %d, want 0", i, s.Levels[i])
		}
	}
	if s.Levels[historyCells-1] != 1000 {
		t.Errorf("newest bucket = %d, want 1000", s.Levels[historyCells-1])
	}
	bars := historyBars(s, 1000)
	if !strings.HasSuffix(bars, gaugeFilled) || strings.HasPrefix(bars, gaugeFilled) {
		t.Errorf("bars = %q, want the fill at the newest (right) end", bars)
	}
}

// The point of Q1: the inspector and the row must be readable against each
// other. A bucket's cell count is asserted to be exactly what the row's gauge
// would draw for the same per-second work rate, so if gauge.go changes its
// scaling, its rounding or its any-work-shows floor, this fails instead of the
// two views silently drifting apart.
func TestHistorySharesRowScale(t *testing.T) {
	now := histNow()
	rowUnit := float64(sparkWindowSeconds) / float64(sparkCells)

	runMax := 700
	for _, level := range []int{0, 1, 40, 81, 82, 200, 700, 820, 4000, 5000} {
		// The same work expressed the way the row measures it. A bucket that
		// holds work is the history's equivalent of a moving row, which is what
		// earns activityGauge's one-cell floor.
		perSecond := float64(level) / float64(historyBucketSeconds)
		share := perSecond / (float64(runMax) / rowUnit)
		want := strings.Count(activityGauge(share, level > 0, now), gaugeFilled)
		if got := historyShareCells(level, runMax, historyBucketSeconds); got != want {
			t.Errorf("level %d vs runMax %d: history %d cells, row %d cells (share %.4f)",
				level, runMax, got, want, share)
		}
		if filled := historyFilled(level, runMax, historyBucketSeconds); filled != (want >= 1) {
			t.Errorf("level %d: filled=%v, row would light %d cells", level, filled, want)
		}
	}

	// The same claim at a COARSER bar. The lifetime series halves its resolution
	// as a run grows and folding to fit the pane coarsens it again, so the rate
	// conversion has to use the width the bar actually covers or every long run's
	// chart would read as busier than its row.
	for _, barSeconds := range []int{20, 40, 90} {
		for _, level := range []int{1, 200, 700, 5000} {
			perSecond := float64(level) / float64(barSeconds)
			share := perSecond / (float64(runMax) / rowUnit)
			want := strings.Count(activityGauge(share, level > 0, now), gaugeFilled)
			if got := historyShareCells(level, runMax, barSeconds); got != want {
				t.Errorf("level %d over %ds vs runMax %d: history %d cells, row %d",
					level, barSeconds, runMax, got, want)
			}
		}
	}
}

// A tiny agent next to a busy one must not be flattered by the history: the
// denominator is the run's, not the agent's, so the peak the note reports
// separates them the way the row's bar length does.
func TestHistoryUsesCrossAgentDenominator(t *testing.T) {
	now := histNow()
	busy := histAgent("busy", now, 30*time.Second)
	quiet := histAgent("quiet", now, 30*time.Second)
	for i := 50; i < 60; i++ {
		busy.SparkBuckets[i] = state.SparkBucket{Tokens: 1000}
		quiet.SparkBuckets[i] = state.SparkBucket{Tokens: 1}
	}
	busy.Tokens, quiet.Tokens = 10000, 10

	runMax := runActivityMax([]state.Agent{busy, quiet}, "tokens")
	bs := historyFor(busy, "tokens", now)
	qs := historyFor(quiet, "tokens", now)

	// Both fill a bucket, exactly as both rows light at least one cell (C8:
	// work that happened never reads as none).
	if !strings.Contains(historyBars(bs, runMax), gaugeFilled) {
		t.Errorf("busy agent's history has no filled bucket: %q", historyBars(bs, runMax))
	}
	if !strings.Contains(historyBars(qs, runMax), gaugeFilled) {
		t.Errorf("quiet agent's history has no filled bucket: %q", historyBars(qs, runMax))
	}
	// The magnitude the two glyphs cannot carry is in the note instead.
	if got := historyPeak(bs, runMax); got != gaugeCells {
		t.Errorf("busiest agent peaks at %d/%d, want the full bar", got, gaugeCells)
	}
	if got := historyPeak(qs, runMax); got != 1 {
		t.Errorf("1000× quieter agent peaks at %d/%d, want 1", got, gaugeCells)
	}
	if note := historyNote(qs, runMax); !strings.Contains(note, "peak 1/5 of run") {
		t.Errorf("note = %q, does not report the run-relative peak", note)
	}
	// Scaled to itself the quiet agent would claim the full bar — the failure
	// the shared denominator exists to prevent.
	if got := historyPeak(qs, 1); got != gaugeCells {
		t.Errorf("sanity: against its own max the quiet agent peaks at %d", got)
	}
}

// Pre-spawn time is not quiet time. An agent alive for 25s gets three buckets,
// not six padded with · — a · says "measured, nothing there" (C8).
func TestHistoryDoesNotPadBeforeSpawn(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 25*time.Second)
	for i := 40; i < 60; i++ {
		a.SparkBuckets[i] = state.SparkBucket{Tokens: 200}
	}
	a.Tokens = 4000

	s := historyFor(a, "tokens", now)
	if len(s.Levels) != 3 {
		t.Fatalf("levels = %d, want 3 buckets for a 25s life", len(s.Levels))
	}
	if s.Clipped {
		t.Error("a 25s life is not clipped by a 60s window")
	}
	if got := historyNote(s, 900); !strings.Contains(got, "whole run") {
		t.Errorf("note = %q, want it to claim the whole run", got)
	}
}

// The horizon must be stated, not implied. state.Agent keeps 60 seconds; an
// agent alive for four minutes is shown a tail and told so.
func TestHistorySaysWhenItIsOnlyATail(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 4*time.Minute+12*time.Second)
	a.SparkBuckets[59] = state.SparkBucket{Tokens: 900}
	a.Tokens = 900

	s := historyFor(a, "tokens", now)
	if !s.Clipped {
		t.Fatal("a 4m12s life must be marked clipped against a 60s window")
	}
	note := historyNote(s, 900)
	for _, want := range []string{"10s/bar", "last 60s of", "4:12"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, missing %q", note, want)
		}
	}
	if strings.Contains(note, "whole run") {
		t.Errorf("note = %q claims the whole run for a clipped window", note)
	}
}

// An agent with no lifetime data renders a stated absence, never a row of ·
// that reads as a measured flatline.
func TestHistoryAbsenceIsStatedNotDrawn(t *testing.T) {
	now := histNow()
	v := newUIState(testConfig())
	pal := NewPalette(2, true)

	never := state.Agent{ID: "never", Name: "queued thing", Status: state.StatusQueued}
	w := state.World{Agents: []state.Agent{never}, Connected: true}
	v.SelID = never.ID
	got := stripANSI(renderSegs(inspectorHistoryRow(w, v, 64, now, pal)))
	if !strings.Contains(got, "no activity recorded") {
		t.Errorf("row = %q, want a stated absence", got)
	}
	if strings.Contains(got, gaugeEmpty+gaugeEmpty) {
		t.Errorf("row = %q, drew a fabricated empty chart", got)
	}
	if strings.Contains(got, gaugeFilled) {
		t.Errorf("row = %q, drew a bar with no data", got)
	}

	// A teammate is a declared low-telemetry class (§3.15) and says so.
	mate := state.Agent{ID: "mate", Name: "scout", Teammate: true, Status: state.StatusTeammate}
	w = state.World{Agents: []state.Agent{mate}, Connected: true}
	v.SelID = mate.ID
	got = stripANSI(renderSegs(inspectorHistoryRow(w, v, 64, now, pal)))
	if !strings.Contains(got, "limited telemetry") {
		t.Errorf("teammate row = %q", got)
	}

	// main keeps no per-second buckets at all.
	v.SelID = "main"
	got = stripANSI(renderSegs(inspectorHistoryRow(state.World{Connected: true}, v, 64, now, pal)))
	if !strings.Contains(got, "root agent") {
		t.Errorf("main row = %q, want the root-agent absence", got)
	}
}

// A tokenless agent with tool calls still gets a history, in the same units the
// row picked (§3.4's tokens→calls fallback).
func TestHistoryFollowsTheRowsMetricFallback(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 30*time.Second)
	for i := 50; i < 60; i++ {
		a.SparkBuckets[i] = state.SparkBucket{Calls: 1}
	}
	a.TotalCalls = 10

	s := historyFor(a, "tokens", now)
	if s.Metric != "calls" {
		t.Fatalf("metric = %q, want the calls fallback", s.Metric)
	}
	rowMetric, _ := effectiveSpark(a.SparkBuckets, "tokens")
	if s.Metric != rowMetric {
		t.Fatalf("history metric %q != row metric %q", s.Metric, rowMetric)
	}
	if !strings.Contains(historyBars(s, runActivityMax([]state.Agent{a}, "tokens")), gaugeFilled) {
		t.Error("a call-only agent drew no history")
	}
}

// The inspector gained a row and the row lost nothing: the history line appears
// inside the same 14-row split, and the agent row's gauge is still 5 cells.
func TestInspectorHistoryRowIsAdditive(t *testing.T) {
	now := histNow()
	v := newUIState(testConfig())
	pal := NewPalette(2, true)
	a := histAgent("a", now, 45*time.Second)
	for i := 30; i < 60; i++ {
		a.SparkBuckets[i] = state.SparkBucket{Tokens: 300}
	}
	a.Tokens = 9000
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s"},
		Agents:    []state.Agent{a},
		Connected: true,
	}
	v.SelID = a.ID
	v.InspectorOpen = true

	lines := renderInspector(w, v, 64, now, pal)
	if len(lines) > inspectorMax {
		t.Fatalf("inspector = %d lines, cap is %d", len(lines), inspectorMax)
	}
	var hist string
	for _, ln := range lines {
		if s := stripANSI(renderSegs(ln)); strings.Contains(s, strings.TrimSpace(historyLabel)) {
			hist = s
		}
	}
	if hist == "" {
		t.Fatal("no history line in the inspector")
	}
	if !strings.Contains(hist, "10s/bar") {
		t.Errorf("history line %q does not state its granularity", hist)
	}
	if n := len([]rune(strings.TrimRight(hist, " "))); n > 64 {
		t.Errorf("history line is %d columns wide", n)
	}
}

// Narrow 44: the line survives and the bars survive; only the note is cut, and
// the cut is marked.
func TestInspectorHistoryNarrow44(t *testing.T) {
	now := histNow()
	v := newUIState(testConfig())
	v.WidthMode = "narrow"
	pal := NewPalette(2, true)
	a := histAgent("a", now, 4*time.Minute)
	for i := range a.SparkBuckets {
		a.SparkBuckets[i] = state.SparkBucket{Tokens: 400}
	}
	a.Tokens = 24000
	w := state.World{Agents: []state.Agent{a}, Connected: true}
	v.SelID = a.ID

	got := stripANSI(renderSegs(inspectorHistoryRow(w, v, 44, now, pal)))
	if n := len([]rune(got)); n > 44 {
		t.Fatalf("row is %d columns: %q", n, got)
	}
	if !strings.Contains(got, gaugeFilled) {
		t.Errorf("row = %q lost its bars", got)
	}
}

// --- the lifetime series (§13.3 Q1) ---

// histLifetimeAgent builds an agent with a lifetime series of `n` 10s buckets,
// every `everyNth` of which holds work. This is what state.Machine now supplies.
func histLifetimeAgent(id string, now time.Time, n, everyNth int) state.Agent {
	step := historyBucketSeconds * time.Second
	span := time.Duration(n) * step
	a := state.Agent{
		ID:          id,
		Name:        "port the routes",
		Type:        "general-purpose",
		Status:      state.StatusRun,
		SpawnedAt:   now.Add(-span),
		LastEventAt: now,
		SparkAt:     now,
		HistoryFrom: now.Add(-span),
		HistoryStep: step,
	}
	for i := 0; i < n; i++ {
		b := state.SparkBucket{}
		if i%everyNth == 0 {
			b.Tokens = 1000
			a.Tokens += 1000
		}
		a.HistoryBuckets = append(a.HistoryBuckets, b)
	}
	// The row's own window sees the tail of the same work.
	for i := 50; i < 60; i++ {
		a.SparkBuckets[i] = state.SparkBucket{Tokens: 100}
	}
	return a
}

// The ruling is "the whole agent lifetime", and the rolling 60s window could not
// supply it: a five-minute agent was shown its last minute and told so. With the
// lifetime series the chart covers the run and the note stops apologising.
func TestHistoryCoversTheWholeLifetimeNotJustTheWindow(t *testing.T) {
	now := histNow()
	a := histLifetimeAgent("a", now, 30, 3) // 30 × 10s = 5 minutes
	s := historyFor(a, "tokens", now).fold(historyDrawCells)

	if s.Clipped {
		t.Error("a series that starts at the spawn bucket is not clipped")
	}
	if len(s.Levels) > historyDrawCells {
		t.Errorf("drew %d bars, budget is %d", len(s.Levels), historyDrawCells)
	}
	if s.Covered() < s.Lifetime {
		t.Errorf("bars cover %s of a %s lifetime", s.Covered(), s.Lifetime)
	}
	note := historyNote(s, 900)
	if !strings.Contains(note, "whole run (5:00)") {
		t.Errorf("note = %q, want it to claim the whole run", note)
	}
	if strings.Contains(note, "last ") {
		t.Errorf("note = %q still describes a tail", note)
	}
	// The granularity is REPORTED, never assumed: 30 source buckets folded into
	// the 8-bar budget is 4 buckets a bar, so a bar is 40s and says so.
	if s.BarSeconds != 40 {
		t.Fatalf("bar = %ds, want 40 (30 buckets over %d bars)", s.BarSeconds, historyDrawCells)
	}
	if !strings.HasPrefix(note, "40s/bar") {
		t.Errorf("note = %q does not lead with the real bar width", note)
	}
}

// Folding groups whole source buckets and loses no work: it trades resolution for
// width, which is the only honest trade available. Clipping would trade span, and
// the span is what the ruling asks for.
func TestHistoryFoldKeepsEveryBucketsWork(t *testing.T) {
	now := histNow()
	a := histLifetimeAgent("a", now, 30, 1)
	full := historyFor(a, "tokens", now)
	if len(full.Levels) != 30 || full.BarSeconds != historyBucketSeconds {
		t.Fatalf("unfolded series = %d bars of %ds", len(full.Levels), full.BarSeconds)
	}
	folded := full.fold(historyDrawCells)
	sum := func(v []int) int {
		n := 0
		for _, x := range v {
			n += x
		}
		return n
	}
	if sum(folded.Levels) != sum(full.Levels) {
		t.Errorf("folding lost work: %d of %d", sum(folded.Levels), sum(full.Levels))
	}
	if folded.BarSeconds != full.BarSeconds*4 {
		t.Errorf("bar = %ds, want 4 source buckets wide", folded.BarSeconds)
	}
	if got := folded.Covered(); got < full.Covered() {
		t.Errorf("folding lost span: %s of %s", got, full.Covered())
	}
	// A series that already fits is returned untouched — no fabricated bars, no
	// restated width.
	short := historyFor(histLifetimeAgent("b", now, 5, 1), "tokens", now).fold(historyDrawCells)
	if len(short.Levels) != 5 || short.BarSeconds != historyBucketSeconds {
		t.Errorf("a 5-bucket series was folded anyway: %d bars of %ds",
			len(short.Levels), short.BarSeconds)
	}
}

// A coarsened series (state halves its own resolution past its cap) must be drawn
// and labelled at the width it really has, not at the nominal 10s.
func TestHistoryReadsTheStepOffTheSeries(t *testing.T) {
	now := histNow()
	a := histLifetimeAgent("a", now, 6, 1)
	a.HistoryStep = 80 * time.Second // as a long run's series arrives
	a.HistoryFrom = now.Add(-8 * time.Minute)
	a.SpawnedAt = a.HistoryFrom
	s := historyFor(a, "tokens", now).fold(historyDrawCells)
	if s.BarSeconds != 80 {
		t.Fatalf("bar = %ds, want the series' own 80s", s.BarSeconds)
	}
	if note := historyNote(s, 900); !strings.HasPrefix(note, "80s/bar") {
		t.Errorf("note = %q claims a granularity the data does not have", note)
	}
	if s.Clipped {
		t.Errorf("6 × 80s covers the 8:00 lifetime, so nothing is clipped")
	}
}

// End to end through the real machine over a run longer than the spark window:
// the inspector row must say "whole run", which is the ruling, and it could not
// before.
func TestInspectorHistoryOverARunLongerThanTheWindow(t *testing.T) {
	start := histNow().Add(-6 * time.Minute)
	m := state.New(state.DefaultConfig())
	m.Apply(event.Event{Kind: event.AgentSpawned, AgentID: "a1", AgentName: "port the routes", At: start}, start)
	tokens := 0
	for d := time.Duration(0); d <= 6*time.Minute; d += 15 * time.Second {
		at := start.Add(d)
		tokens += 400
		m.Apply(event.Event{Kind: event.TokensUpdated, AgentID: "a1", Tokens: tokens, At: at}, at)
	}
	now := start.Add(6 * time.Minute)
	m.Tick(now)
	w := m.Snapshot()

	v := newUIState(testConfig())
	v.SelID = "a1"
	pal := NewPalette(2, true)
	got := stripANSI(renderSegs(inspectorHistoryRow(w, v, 64, now, pal)))

	if !strings.Contains(got, "whole run (6:00)") {
		t.Errorf("row = %q, want the whole run", got)
	}
	if strings.Contains(got, "last 60s") {
		t.Errorf("row = %q is still drawing the rolling window", got)
	}
	if !strings.Contains(got, gaugeFilled) {
		t.Errorf("row = %q drew no bars for six minutes of work", got)
	}
	if n := len([]rune(strings.TrimRight(got, " "))); n > 64 {
		t.Errorf("row is %d columns: %q", n, got)
	}
	// The bar width has to be a whole multiple of the source bucket, and stated.
	a := w.Agents[0]
	s := historyFor(a, v.SparkMetric, now).fold(historyDrawCells)
	if s.BarSeconds%int(a.HistoryStep/time.Second) != 0 {
		t.Errorf("bar %ds is not a multiple of the %s source bucket", s.BarSeconds, a.HistoryStep)
	}
	if !strings.Contains(got, itoa(s.BarSeconds)+"s/bar") {
		t.Errorf("row = %q does not state the %ds bar it drew", got, s.BarSeconds)
	}
}

// A world with no lifetime series still renders — and still admits that what it is
// showing is a tail. The fallback is the only thing that kept the old ruling
// unmet, so it may not pretend otherwise.
func TestHistoryFallbackStillAdmitsItIsATail(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 4*time.Minute+12*time.Second)
	a.SparkBuckets[59] = state.SparkBucket{Tokens: 900}
	a.Tokens = 900
	if len(a.HistoryBuckets) != 0 {
		t.Fatal("fixture is not the fallback case")
	}
	s := historyFor(a, "tokens", now).fold(historyDrawCells)
	if !s.Clipped {
		t.Fatal("the 60s window over a 4m12s life is a tail")
	}
	note := historyNote(s, 900)
	for _, want := range []string{"10s/bar", "last 60s of", "4:12"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, missing %q", note, want)
		}
	}
}

// The only glyphs Q5 leaves for a gauge are ▇ and ·; the withdrawn ▁▂▃▄▅▆ must
// never appear in the history.
func TestHistoryUsesOnlyTheAllowedGlyphs(t *testing.T) {
	now := histNow()
	a := histAgent("a", now, 60*time.Second)
	for i := range a.SparkBuckets {
		if i%20 == 0 {
			a.SparkBuckets[i] = state.SparkBucket{Tokens: 900}
		}
	}
	a.Tokens = 2700
	s := historyFor(a, "tokens", now)
	bars := historyBars(s, 300)
	for _, r := range bars {
		if r != []rune(gaugeFilled)[0] && r != []rune(gaugeEmpty)[0] {
			t.Fatalf("bars %q contain %q, outside the Q5 gauge set", bars, string(r))
		}
	}
	for _, w := range []string{"▁", "▂", "▃", "▄", "▅", "▆"} {
		if strings.Contains(bars, w) {
			t.Fatalf("withdrawn glyph %q in %q", w, bars)
		}
	}
}
