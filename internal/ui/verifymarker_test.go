package ui

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

// TestVerifyMarkerSplitsOnExitCode is §13.3 Q6 on the row: a zero exit reads
// nothing (a passing check is not news), a non-zero one "✖ verify failed", and a check still running
// says nothing — there is no outcome to report yet.
func TestVerifyMarkerSplitsOnExitCode(t *testing.T) {
	now := colNow()
	pal := NewPalette(2, false)

	cases := []struct {
		name   string
		verify state.VerifyState
		want   string
	}{
		{"zero exit", state.VerifyOK, ""}, // passing is not news; the inspector has it
		{"non-zero exit", state.VerifyFailed, "✖ verify failed"},
		{"still running", state.VerifyNone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := degradeAgent(now)
			a.Verify = c.verify
			got, _ := verifyMarker(a, pal)
			if got != c.want {
				t.Errorf("marker = %q, want %q", got, c.want)
			}

			row := strings.Join(rowLines(t, a, 64, true, now), "\n")
			if c.want == "" {
				// Neither an open check nor a passing one puts anything on the
				// row: only a failure is news.
				for _, absent := range []string{"verified", "verify failed"} {
					if strings.Contains(row, absent) {
						t.Errorf("the row claimed %q:\n%s", absent, row)
					}
				}
				return
			}
			if !strings.Contains(row, c.want) {
				t.Errorf("row lost the marker:\n%s", row)
			}
			// It lives on the activity line, never on the pips.
			for _, ln := range rowLines(t, a, 64, true, now) {
				if strings.Contains(ln, c.want) && !strings.Contains(ln, nowMark) {
					t.Errorf("the marker escaped the activity line: %q", ln)
				}
				if strings.Contains(ln, c.want) && strings.Contains(ln, "▪▪▫") &&
					strings.Index(ln, c.want) > strings.Index(ln, "▪▪▫") {
					t.Errorf("the marker landed after the pips: %q", ln)
				}
			}
		})
	}
}

// A failed verification is loud but is still not a station: the pips are
// unchanged by either outcome, because a station means "observed fact" and this
// is an inference (§13.3 Q6).
func TestVerifyMarkerNeverTouchesThePips(t *testing.T) {
	now := colNow()
	base := degradeAgent(now)
	want := pips(base.Station, false)
	for _, vs := range []state.VerifyState{state.VerifyNone, state.VerifyOK, state.VerifyFailed} {
		a := base
		a.Verify = vs
		row := strings.Join(rowLines(t, a, 64, true, now), "\n")
		if !strings.Contains(row, want) {
			t.Errorf("verify=%v changed the pips (want %q):\n%s", vs, want, row)
		}
	}
}
