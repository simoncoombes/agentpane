package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// §3.4 scales each agent against its own 60s max, but that max drops the
// moment a tall bucket rolls out of the window, and every remaining bar grows
// to fill the new scale — the work did not change, the yardstick did. A fallen
// max is held; a rising one is adopted at once, because a new peak is real.
func TestSparkScaleHoldsAFallenMax(t *testing.T) {
	v := newUIState(testConfig())
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	if got := v.sparkFloor("a1", "tokens", 100, now); got != 100 {
		t.Fatalf("first observation must set the scale: %d", got)
	}
	// The peak rolls out of the window; the scale must not follow immediately.
	if got := v.sparkFloor("a1", "tokens", 10, now.Add(time.Second)); got != 100 {
		t.Errorf("fallen max adopted too early: %d, want 100 held", got)
	}
	// Still held just before the hold expires.
	if got := v.sparkFloor("a1", "tokens", 10, now.Add(sparkHold-time.Millisecond)); got != 100 {
		t.Errorf("hold expired early: %d", got)
	}
	// Adopted once the hold lapses.
	if got := v.sparkFloor("a1", "tokens", 10, now.Add(sparkHold)); got != 10 {
		t.Errorf("fallen max never adopted: %d, want 10", got)
	}
	// A rising max is never clipped, whenever it arrives.
	if got := v.sparkFloor("a1", "tokens", 500, now.Add(sparkHold+time.Second)); got != 500 {
		t.Errorf("a new peak must be adopted at once: %d", got)
	}
	// Agents are scaled independently.
	if got := v.sparkFloor("a2", "tokens", 3, now); got != 3 {
		t.Errorf("scales must not leak between agents: %d", got)
	}
}

// The floor may raise the scale but must never draw where there is no data,
// nor clip a real bar. An all-zero window is not enough to prove this — every
// cell renders · whatever the floor — so this also drives one small bucket
// against a large floor: the empty cells must stay ·, and the drawn cell must
// shrink to the floor's scale rather than filling the row.
func TestSparkFloorDoesNotInventBars(t *testing.T) {
	var empty [60]state.SparkBucket
	if got := sparklineScaled(empty, "tokens", 100); got != flatline {
		t.Errorf("a floor must not draw bars from nothing: %q", got)
	}

	var one [60]state.SparkBucket
	one[59] = state.SparkBucket{Tokens: 10}
	unscaled := sparklineScaled(one, "tokens", 0)
	scaled := sparklineScaled(one, "tokens", 1000)
	if strings.Count(scaled, "·") != strings.Count(unscaled, "·") {
		t.Errorf("floor changed which cells are empty: %q vs %q", scaled, unscaled)
	}
	if scaled == unscaled {
		t.Errorf("a 100x floor must shrink the drawn bar, got %q for both", scaled)
	}
	if !strings.ContainsRune(scaled, '▁') {
		t.Errorf("bar should sit at the bottom of a 100x scale, got %q", scaled)
	}
}

// A held token-scale must not be applied to a call count. sparkline falls back
// from tokens to calls when every token bucket is empty; a floor of 800 tokens
// against a 3-call window flattens every cell to ▁ — the wrong yardstick,
// which is the jitter the hold exists to remove.
func TestSparkScaleDoesNotSurviveTheMetricFallback(t *testing.T) {
	v := newUIState(testConfig())
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	if got := v.sparkFloor("a1", "tokens", 800, now); got != 800 {
		t.Fatalf("token scale = %d, want 800", got)
	}
	// The window flips to calls; the token floor must not follow it over.
	if got := v.sparkFloor("a1", "calls", 3, now.Add(time.Second)); got != 3 {
		t.Errorf("token floor leaked onto a call window: %d, want 3", got)
	}
	// Each metric still holds its own fallen max independently.
	if got := v.sparkFloor("a1", "tokens", 10, now.Add(2*time.Second)); got != 800 {
		t.Errorf("token hold lost across the flip: %d, want 800 held", got)
	}
}
