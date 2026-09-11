package ui

import (
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// A busy agent making sub-second calls must not flip its right column between
// the sparkline and "◐ in Bash" on every frame (§3.13: no animation).
func TestOpenCallIndicatorWaitsForDwell(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		open time.Duration
		want bool
	}{
		{"just opened", 0, false},
		{"quick call", 200 * time.Millisecond, false},
		{"just under dwell", openCallDwell - time.Millisecond, false},
		{"at dwell", openCallDwell, true},
		{"long build", 3 * time.Minute, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := state.Agent{OpenCall: &state.OpenCall{Tool: "Bash", Since: now.Add(-c.open)}}
			if got := showOpenCall(a, now); got != c.want {
				t.Errorf("open %v: showOpenCall = %v, want %v", c.open, got, c.want)
			}
		})
	}
	if showOpenCall(state.Agent{}, now) {
		t.Error("no open call must never show the indicator")
	}
}
