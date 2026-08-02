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

// The whole point of a shared scale: an agent doing twice the work of another
// must draw a longer bar. Under the old per-agent scaling both drew full.
func TestGaugeIsComparableAcrossAgents(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	busy := state.Agent{ID: "busy", Status: state.StatusRun, LastEventAt: now}
	quiet := state.Agent{ID: "quiet", Status: state.StatusRun, LastEventAt: now}
	busy.SparkBuckets[59] = state.SparkBucket{Tokens: 1000}
	quiet.SparkBuckets[59] = state.SparkBucket{Tokens: 100}

	runMax := runActivityMax([]state.Agent{busy, quiet}, "tokens")
	if runMax != 1000 {
		t.Fatalf("runMax = %d, want the busiest agent's level", runMax)
	}
	b := strings.Count(gaugeFor(busy, "tokens", runMax, now), gaugeFilled)
	q := strings.Count(gaugeFor(quiet, "tokens", runMax, now), gaugeFilled)
	if b <= q {
		t.Errorf("busier agent must draw a longer bar: busy=%d quiet=%d", b, q)
	}
	if b != gaugeCells {
		t.Errorf("the busiest agent should fill the bar, got %d", b)
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

// Motion is never decorative: a still agent's row must be still.
func TestGaugeStopsMovingWhenTheAgentDoes(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	a := state.Agent{ID: "a", Status: state.StatusRun, LastEventAt: now.Add(-gaugeIdle - time.Second)}
	a.SparkBuckets[59] = state.SparkBucket{Tokens: 10}
	if got := gaugeFor(a, "tokens", 10, now); !strings.HasPrefix(got, " ") {
		t.Errorf("a silent agent must not spin: %q", got)
	}
	a.LastEventAt = now
	if got := gaugeFor(a, "tokens", 10, now); strings.HasPrefix(got, " ") {
		t.Errorf("a producing agent must spin: %q", got)
	}
}
