package ui

import (
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

func TestSparklineScaling(t *testing.T) {
	var b [60]state.SparkBucket

	if got := sparkline(b, "tokens"); got != flatline {
		t.Errorf("empty buckets = %q, want flatline", got)
	}

	// One cell at max, one at half, empties as dots. Cell i covers buckets
	// [i*60/7, (i+1)*60/7).
	b[0].Tokens = 700  // cell 0
	b[30].Tokens = 350 // cell 3
	got := sparkline(b, "tokens")
	r := []rune(got)
	if len(r) != sparkCells {
		t.Fatalf("width %d, want %d", len(r), sparkCells)
	}
	if r[0] != '▇' {
		t.Errorf("max cell = %q, want ▇", string(r[0]))
	}
	if r[3] != '▄' {
		t.Errorf("half cell = %q, want ▄ (ceil scaling)", string(r[3]))
	}
	for _, i := range []int{1, 2, 4, 5, 6} {
		if r[i] != '·' {
			t.Errorf("cell %d = %q, want ·", i, string(r[i]))
		}
	}

	// Per-agent max scale: a tiny agent's max still renders full height.
	var small [60]state.SparkBucket
	small[59].Tokens = 1
	if r := []rune(sparkline(small, "tokens")); r[6] != '▇' {
		t.Errorf("per-agent scaling broken: %q", string(r))
	}
}

func TestSparklineCallsMetricAndFallback(t *testing.T) {
	var b [60]state.SparkBucket
	b[10].Calls = 2
	b[55].Calls = 4

	// tokens metric with zero token buckets falls back to calls (§3.4).
	got := sparkline(b, "tokens")
	if got == flatline {
		t.Fatalf("token metric did not fall back to calls: %q", got)
	}
	if got != sparkline(b, "calls") {
		t.Errorf("fallback %q != calls metric %q", got, sparkline(b, "calls"))
	}

	// With token data present, tokens win and calls are ignored.
	b[20].Tokens = 100
	tok := sparkline(b, "tokens")
	if tok == sparkline(b, "calls") {
		t.Errorf("token metric ignored token data")
	}
}

func TestTokensLastMinute(t *testing.T) {
	var b [60]state.SparkBucket
	b[0].Tokens = 100
	b[59].Tokens = 20
	if got := tokensLastMinute(b); got != 120 {
		t.Errorf("tokensLastMinute = %d, want 120", got)
	}
}
