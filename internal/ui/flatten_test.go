package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// A multi-line Bash command must never break the frame. `python3 -c` one-liners
// routinely embed newlines; runewidth counts them as zero columns, so neither
// the width budget nor truncSegs saw them and one row became several physical
// lines — the tree overflowed the pane and the terminal scrolled.
func TestMultilineToolTextCannotBreakTheFrame(t *testing.T) {
	const nasty = "/usr/bin/python3 -c \"\nimport json,re\np='/Users/x/very/long/path'\nprint(p)\n\"\ttrailing"

	m := state.New(state.DefaultConfig())
	now := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	apply := func(k event.Kind, mut func(*event.Event)) {
		ev := event.Event{Seq: uint64(k) + 1, At: now, AgentID: "a1", Kind: k}
		if mut != nil {
			mut(&ev)
		}
		m.Apply(ev, now)
	}
	apply(event.AgentSpawned, func(e *event.Event) { e.AgentName = "Run a python one-liner" })
	apply(event.AgentStarted, nil)
	apply(event.ToolStart, func(e *event.Event) { e.Tool, e.Target = "Bash", nasty })
	apply(event.ToolEnd, func(e *event.Event) { e.Tool, e.Target = "Bash", nasty })

	w := m.Snapshot()
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	if len(w.Agents) != 1 {
		t.Fatalf("want the agent rendered, got %d", len(w.Agents))
	}
	v.SelID = w.Agents[0].ID

	for _, cols := range []int{44, 64, 100} {
		for _, rows := range []int{12, 20, 44} {
			frame, _ := renderFrame(w, v, cols, rows, now, NewPalette(2, false))
			plain := stripANSI(frame)
			lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
			if len(lines) > rows {
				t.Fatalf("%dx%d: %d lines — multi-line text broke the frame:\n%s",
					cols, rows, len(lines), plain)
			}
			for i, ln := range lines {
				if strings.ContainsAny(ln, "\t\r\v\f") {
					t.Errorf("%dx%d line %d still carries a control char: %q", cols, rows, i, ln)
				}
				if wdt := runewidth.StringWidth(ln); wdt > cols {
					t.Errorf("%dx%d line %d is %d columns wide: %q", cols, rows, i, wdt, ln)
				}
			}
		}
	}
}

func TestFlatten(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\nb", "a b"},
		{"a\r\nb", "a  b"},
		{"a\tb", "a b"},
		{"esc\x1b[31mred", "esc[31mred"},
		{"bell\x07", "bell"},
		{"", ""},
	}
	for _, c := range cases {
		if got := flatten(c.in); got != c.want {
			t.Errorf("flatten(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
