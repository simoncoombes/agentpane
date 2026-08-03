package ui

import (
	"strings"
	"testing"
	"time"

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
		if got := historyShareCells(level, runMax); got != want {
			t.Errorf("level %d vs runMax %d: history %d cells, row %d cells (share %.4f)",
				level, runMax, got, want, share)
		}
		if filled := historyFilled(level, runMax); filled != (want >= 1) {
			t.Errorf("level %d: filled=%v, row would light %d cells", level, filled, want)
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
