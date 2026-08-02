package ui

import "github.com/simoncoombes/agentpane/internal/state"

// sparkCells is the §3.4 geometry: 7 cells over the trailing 60s.
const sparkCells = 7

var sparkGlyphs = []rune("▁▂▃▄▅▆▇")

// flatline is the all-empty sparkline (§2.6a: nothing is running and nothing
// has happened).
const flatline = "·······"

// sparkline renders 7 cells from the agent's 60 one-second buckets, scaled
// against the agent's own max (§3.4). metric is "tokens" or "calls"; when
// every token bucket is zero but calls exist, it falls back to calls
// automatically (§3.4). Empty buckets render ·. Never interpolated (C8).
func sparkline(buckets [60]state.SparkBucket, metric string) string {
	return sparklineScaled(buckets, metric, 0)
}

// sparklineScaled draws against at least floor, so a caller can hold a fallen
// maximum steady (see UIState.sparkScale). floor 0 means "scale to the window",
// the §3.4 rule.
func sparklineScaled(buckets [60]state.SparkBucket, metric string, floor int) string {
	vals := bucketize(buckets, metric)
	if metric != "calls" && allZero(vals) {
		if calls := bucketize(buckets, "calls"); !allZero(calls) {
			vals = calls
		}
	}
	max := 0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return flatline
	}
	if floor > max {
		max = floor
	}
	out := make([]rune, sparkCells)
	for i, v := range vals {
		if v == 0 {
			out[i] = '·'
			continue
		}
		lvl := (v*len(sparkGlyphs) + max - 1) / max // ceil, 1..7
		if lvl < 1 {
			lvl = 1
		}
		if lvl > len(sparkGlyphs) {
			lvl = len(sparkGlyphs)
		}
		out[i] = sparkGlyphs[lvl-1]
	}
	return string(out)
}

// bucketize folds 60 one-second buckets into 7 cells of ~8.5s each.
func bucketize(buckets [60]state.SparkBucket, metric string) [sparkCells]int {
	var out [sparkCells]int
	for i := 0; i < sparkCells; i++ {
		start := i * 60 / sparkCells
		end := (i + 1) * 60 / sparkCells
		for j := start; j < end; j++ {
			if metric == "calls" {
				out[i] += buckets[j].Calls
			} else {
				out[i] += buckets[j].Tokens
			}
		}
	}
	return out
}

func allZero(vals [sparkCells]int) bool {
	for _, v := range vals {
		if v != 0 {
			return false
		}
	}
	return true
}

// tokensLastMinute sums the token deltas in the trailing 60s window, the
// basis for the §3.18 burn rate.
func tokensLastMinute(buckets [60]state.SparkBucket) int {
	n := 0
	for _, b := range buckets {
		n += b.Tokens
	}
	return n
}

// effectiveSpark reports the metric the sparkline will ACTUALLY draw (after
// the tokens→calls fallback) and that window's max. The caller needs both:
// holding a scale across a metric flip would apply a token-sized floor to a
// call count.
func effectiveSpark(buckets [60]state.SparkBucket, metric string) (string, int) {
	vals := bucketize(buckets, metric)
	if metric != "calls" && allZero(vals) {
		if calls := bucketize(buckets, "calls"); !allZero(calls) {
			vals, metric = calls, "calls"
		}
	}
	max := 0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	return metric, max
}
