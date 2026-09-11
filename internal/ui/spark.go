package ui

import "github.com/simoncoombes/agentpane/internal/state"

// The §3.4 row sparkline is WITHDRAWN (§13.3 Q5), and `▁▂▃▄▅▆` went with it:
// the glyph table is closed, the row draws the shared-scale gauge instead
// (gauge.go), and no render path may reintroduce a per-agent scale. What
// survives here is the bucket arithmetic every consumer still needs — the
// gauge's level, the §3.18 burn rate, and the inspector's own history — so
// there is exactly one definition of "how much has this agent done lately".
//
// sparkCells stays as the folding width of that arithmetic: 7 cells over the
// trailing 60s, which is what effectiveSpark reports a maximum for.
const sparkCells = 7

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

// effectiveSpark reports the metric the activity level is ACTUALLY measured in
// (after the tokens→calls fallback, §3.4) and that window's max. Callers need
// both: a token-sized number and a call count are not comparable, so anything
// holding or sharing a scale has to know which one it is looking at.
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
