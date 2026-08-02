package ui

import (
	"fmt"
	"strings"
	"time"
)

// formatSince renders the §2.6 liveness clock: `3s`, `2m50s`, `1h04m`.
func formatSince(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
}

// formatClock renders m:ss (inspector elapsed, decayed tails, run header).
func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// formatTokens renders a token count: 112k, 44k, 611k. Zero renders empty
// (honest absence, C8); sub-1000 renders the raw count.
func formatTokens(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
}

// formatRate renders the §3.18 burn rate behind `t`: `2.1k/s`.
func formatRate(tokensPerMinute int) string {
	return fmt.Sprintf("%.1fk/s", float64(tokensPerMinute)/60/1000)
}

// shortPath applies the §2.7 shortening: strip a leading src/ (the repo root
// is already stripped by the source).
func shortPath(p string) string {
	return strings.TrimPrefix(p, "src/")
}

// shortSessionID is the 4-char session id the header and session list use
// (§3.8a), applied here to an id the ui was handed directly (--session).
func shortSessionID(id string) string {
	if len(id) > 4 {
		return id[:4]
	}
	return id
}

// stationNames are the §2.2 observed stations.
var stationNames = [4]string{"spawn", "first tool", "first edit", "return"}

// pips renders the four §2.2 station pips: ▪ reached · ▫ current · · not
// reached. done means the return station itself completed (all four filled).
func pips(station int, done bool) string {
	if station < 0 {
		station = 0
	}
	if station > 3 {
		station = 3
	}
	reached := station
	if done {
		reached = 4
	}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		switch {
		case i < reached:
			b.WriteString("▪")
		case i == reached:
			b.WriteString("▫")
		default:
			b.WriteString("·")
		}
	}
	return b.String()
}

// stationLabel names the current station; a returned agent reads "returned".
func stationLabel(station int, done bool) string {
	if done {
		return "returned"
	}
	if station < 0 {
		station = 0
	}
	if station > 3 {
		station = 3
	}
	return stationNames[station]
}

// spinnerFrames is the §3.11 braille spinner, stepped on the 1s tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}

func spinner(now time.Time) string {
	i := int(now.Unix() % int64(len(spinnerFrames)))
	if i < 0 {
		i += len(spinnerFrames)
	}
	return spinnerFrames[i]
}
