package ui

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/event"
)

// The trunk must be one unbroken line at every width and every agent count,
// with no stub dangling below the final row (§3.2, PART 10).
func TestTrunkUnbroken(t *testing.T) {
	now := demoNow()
	for _, cols := range []int{44, 62, 64, 100, 200} {
		for _, n := range []int{1, 2, 8, 40} {
			w := syntheticWorld(n, now)
			v := newUIState(testConfig())
			v.SelID = event.MainAgentID
			rows := 2*n + 30
			frame, meta := renderFrame(w, v, cols, rows, now, NewPalette(2, false))
			lines := strings.Split(frame, "\n")

			// The tree region ends where the returned-agents list begins: that
			// section is drawn below the trunk and is not part of it.
			first, last := -1, -1
			for i, id := range meta.lineIDs {
				if id == selFinished {
					break
				}
				if id == event.MainAgentID && first == -1 {
					first = i
				}
				if id != "" && id != selBand {
					last = i
				}
			}
			if first == -1 || last == -1 {
				t.Fatalf("cols=%d n=%d: no tree region found", cols, n)
			}

			elbows, branches := 0, 0
			lastElbow := -1
			for i := first; i <= last; i++ {
				txt := stripANSI(lines[i])
				if txt == "" {
					t.Fatalf("cols=%d n=%d: blank line %d inside the tree", cols, n, i)
				}
				r := []rune(txt)[0]
				switch {
				case strings.HasPrefix(txt, branchLast):
					elbows++
					lastElbow = i
				case strings.HasPrefix(txt, branchMid):
					branches++
				}
				if i == first {
					if r != '◍' {
						t.Fatalf("cols=%d n=%d: main row starts %q", cols, n, txt)
					}
					continue
				}
				if i < lastElbow || lastElbow == -1 {
					// Above the elbow every line must carry the trunk.
					if r != '│' && r != '├' && r != '╰' {
						t.Errorf("cols=%d n=%d line %d: trunk broken: %q", cols, n, i, txt)
					}
				} else if i > lastElbow {
					// Below the elbow: no stub — the trunk has ended.
					if r == '│' || r == '├' {
						t.Errorf("cols=%d n=%d line %d: stub below the elbow: %q", cols, n, i, txt)
					}
				}
			}
			if elbows != 1 {
				t.Errorf("cols=%d n=%d: %d elbow lines, want exactly 1", cols, n, elbows)
			}
			// The returned agents are filtered off the tree by default, so the
			// branch count is what the tree DRAWS, not what the world holds.
			if want := len(v.orderedAgents(w)) - 1; branches != want {
				t.Errorf("cols=%d n=%d: %d mid branches, want %d", cols, n, branches, want)
			}
			// Below the region nothing may continue the trunk.
			for i := last + 1; i < len(lines); i++ {
				txt := stripANSI(lines[i])
				if txt == "" {
					continue
				}
				r := []rune(txt)[0]
				if r == '│' || r == '├' || r == '╰' {
					t.Errorf("cols=%d n=%d line %d: trunk glyph below the tree: %q", cols, n, i, txt)
				}
			}
		}
	}
}

// No agent name is ever truncated (§3.1, C4): every slug must appear intact
// at 44 columns.
func TestSlugNeverTruncatedAt44(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.WidthMode = "narrow"
	frame, _ := renderFrame(w, v, 44, 44, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	for _, a := range v.orderedAgents(w) {
		sl := v.slugFor(a)
		if !strings.Contains(plain, sl) {
			t.Errorf("slug %q truncated or missing at 44 cols", sl)
		}
	}
}
