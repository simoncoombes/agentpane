package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// realTitle is verbatim from ~/.claude/projects on this machine: 60 columns,
// with a real em-dash. It is the case that broke the row.
const realTitle = "Build agentpane — live TUI monitor for Claude Code subagents"

// mainLabel's priority chain: session title → run prompt → main activity →
// empty. Nothing is invented when all three are absent (C8).
func TestMainLabelPriority(t *testing.T) {
	cases := []struct {
		name  string
		world state.World
		want  string
	}{
		{
			name: "title wins over prompt and activity",
			world: state.World{
				Main: state.Main{Title: realTitle, Activity: "Bash go test"},
				Run:  &state.Run{Prompt: "please refactor the auth middleware and then..."},
			},
			want: realTitle,
		},
		{
			name: "no title falls back to the run prompt",
			world: state.World{
				Main: state.Main{Activity: "Bash go test"},
				Run:  &state.Run{Prompt: "refactor auth"},
			},
			want: "refactor auth",
		},
		{
			name:  "no title and no run falls back to activity",
			world: state.World{Main: state.Main{Activity: "Bash go test"}},
			want:  "Bash go test",
		},
		{
			name:  "an empty run prompt does not shadow activity",
			world: state.World{Main: state.Main{Activity: "Bash go test"}, Run: &state.Run{}},
			want:  "Bash go test",
		},
		{
			name:  "nothing observed yet is empty, never a placeholder",
			world: state.World{},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainLabel(tc.world); got != tc.want {
				t.Errorf("mainLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// budgetLabel cuts on a word boundary where one exists, mid-word only when a
// single word overruns the budget on its own, and never inside a rune.
func TestBudgetLabelTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "fits untouched", in: "refactor auth", max: 20, want: "refactor auth"},
		{name: "exact fit keeps every word", in: "refactor auth", max: 13, want: "refactor auth"},
		{name: "one column short cuts to a word", in: "refactor auth", max: 12, want: "refactor…"},
		{name: "word boundary, not mid-word", in: realTitle, max: 51, want: "Build agentpane — live TUI monitor for Claude Code…"},
		{name: "narrow budget still lands on a word", in: realTitle, max: 31, want: "Build agentpane — live TUI…"},
		{name: "cut just after the em-dash keeps the dash whole", in: realTitle, max: 19, want: "Build agentpane —…"},
		{name: "single word longer than the budget cuts mid-word", in: "supercalifragilistic", max: 10, want: "supercali…"},
		{name: "first word overruns so no boundary exists", in: "supercalifragilistic run", max: 12, want: "supercalifr…"},
		{name: "non-ASCII cut stays rune-safe", in: "über—straße größer köln", max: 12, want: "über—straße…"},
		{name: "wide runes are measured in columns", in: "日本語 の テスト です", max: 8, want: "日本語…"},
		{name: "budget of one is just the ellipsis", in: realTitle, max: 1, want: "…"},
		{name: "no budget yields nothing", in: realTitle, max: 0, want: ""},
		{name: "negative budget yields nothing", in: realTitle, max: -3, want: ""},
		{name: "empty in, empty out", in: "", max: 40, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := budgetLabel(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("budgetLabel(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if w := runewidth.StringWidth(got); tc.max > 0 && w > tc.max {
				t.Errorf("result is %d columns, over the %d budget", w, tc.max)
			}
			if !utf8Valid(got) {
				t.Errorf("cut landed inside a rune: %q", got)
			}
		})
	}
}

// The label yields first, exactly like the other right-hand elements in
// §3.1's degradation order: the tokens column never moves and the row never
// wraps, at any width.
func TestMainRowLabelNeverPushesTokensOff(t *testing.T) {
	now := demoNow()
	for _, cols := range []int{38, 44, 50, 62, 64, 100} {
		w := syntheticWorld(2, now)
		w.Main.Title = realTitle
		v := newUIState(testConfig())
		v.SelID = "nothing-selected"
		lines := mainBlock(w, v, w.Agents, cols, now, NewPalette(2, false))
		row := stripANSI(renderSegs(lines[0]))
		if got := runewidth.StringWidth(row); got != cols {
			t.Errorf("cols=%d: main row is %d columns wide: %q", cols, got, row)
		}
		if strings.Contains(row, "\n") {
			t.Errorf("cols=%d: main row wrapped: %q", cols, row)
		}
		tok := formatTokens(w.Main.Tokens)
		if !strings.HasSuffix(row, tok) {
			t.Errorf("cols=%d: label pushed the tokens column %q off the row: %q", cols, tok, row)
		}
		if !strings.HasPrefix(row, "◍ main") {
			t.Errorf("cols=%d: row head damaged: %q", cols, row)
		}
		// The label is present but budgeted, and never mid-word.
		if !strings.Contains(row, "Build") {
			t.Errorf("cols=%d: label dropped entirely: %q", cols, row)
		}
		if cols < 74 && !strings.Contains(row, "…") {
			t.Errorf("cols=%d: a 60-column title was not budgeted: %q", cols, row)
		}
	}
}

// At the two spec widths the label lands on a word boundary and the whole
// title stays reachable in the inspector (PART 4) and on `y`.
func TestMainRowLabelAt64And44(t *testing.T) {
	now := demoNow()
	for _, tc := range []struct {
		cols int
		want string
	}{
		{64, "◍ main  Build agentpane — live TUI monitor for Claude Code… 100k"},
		{44, "◍ main  Build agentpane — live TUI…     100k"},
	} {
		w := syntheticWorld(2, now)
		w.Main.Title = realTitle
		w.Main.Tokens = 100000
		v := newUIState(testConfig())
		v.SelID = "nothing-selected"
		lines := mainBlock(w, v, w.Agents, tc.cols, now, NewPalette(2, false))
		if got := stripANSI(renderSegs(lines[0])); got != tc.want {
			t.Errorf("cols=%d\n got %q\nwant %q", tc.cols, got, tc.want)
		}
	}
}

// The inspector and the yank keys are the escape hatch: a budgeted row must
// never be the only copy of the title.
func TestFullTitleReachableWhenMainSelected(t *testing.T) {
	m, _ := newTestModel(t)
	m.world = syntheticWorld(2, m.clock)
	m.world.Main.Title = realTitle // the run prompt is "big refactor"
	m.v.SelID = event.MainAgentID
	m.v.InspectorOpen = true
	frame, _ := renderFrame(m.world, m.v, 64, 40, m.clock, m.pal)
	plain := stripANSI(frame)
	if !strings.Contains(plain, realTitle) {
		t.Errorf("the full title is not in the inspector:\n%s", plain)
	}
	// The row itself is still budgeted — the inspector is the only place the
	// whole thing appears.
	if strings.Count(plain, realTitle) != 1 {
		t.Errorf("expected the full title exactly once (inspector), got %d:\n%s", strings.Count(plain, realTitle), plain)
	}
	for _, key := range []string{"y", "Y"} {
		m.v.Toast = toast{}
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if m.v.Toast.Refusal || !strings.HasPrefix(m.v.Toast.Text, "yanked: Build agentpane") {
			t.Errorf("%s on main toasted %+v", key, m.v.Toast)
		}
	}
}

// utf8Valid reports whether a cut left a replacement character behind.
func utf8Valid(s string) bool {
	return !strings.ContainsRune(s, utf8.RuneError)
}

// The §3.17 expansion budget: pins degrade (4→2→0 calls, then collapse) so
// the tree never scrolls because of pins, and the frame never exceeds rows.
func TestExpansionBudgetNeverExceedsRows(t *testing.T) {
	m, events := demoMachine(t)
	for _, rows := range []int{16, 20, 24, 30, 44} {
		v, w := demoView(t, m, events)
		// Pin everything expandable.
		for _, a := range w.Agents {
			if expandable(a) {
				v.PinSeq++
				v.Pins[a.ID] = v.PinSeq
			}
		}
		frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
		lines := strings.Split(frame, "\n")
		if len(lines) != rows {
			t.Errorf("rows=%d: frame has %d lines", rows, len(lines))
		}
	}
}

// Pinning must degrade instead of scrolling (§3.17): with enough rows for the
// collapsed tree, the footer never shows a scroll range no matter how many
// pins exist.
func TestPinsNeverScroll(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	for _, a := range w.Agents {
		if expandable(a) {
			v.PinSeq++
			v.Pins[a.ID] = v.PinSeq
		}
	}
	// Enough rows for main(2) + 8 collapsed agents (16) + selected minimal
	// expansion + chrome, but not for every pin.
	//
	// This was 36 while the band, the standup and the contention block still sat
	// below the tree. Removing them handed the tree about ten more rows, so the
	// height at which the pin budget actually bites came down with it: at 36 every
	// pin now fits and the degradation this test exists to check never happens.
	rows := 26
	frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	if strings.Contains(plain, " / 8") {
		t.Fatalf("pins caused a scroll window:\n%s", plain)
	}
	// The collapsed pins are visibly marked.
	if !strings.Contains(plain, copyPinnedNoRoom) {
		t.Errorf("expected %q on a collapsed pin", copyPinnedNoRoom)
	}
	// Every agent the tree is showing is still on screen. The returned ones
	// are hidden by default and accounted for by their own line, not by a row.
	for _, a := range v.orderedAgents(w) {
		if !strings.Contains(plain, v.slugFor(a)) {
			t.Errorf("agent %q pushed off screen by pins", v.slugFor(a))
		}
	}
	if n := v.finishedHidden(w); n > 0 && !strings.Contains(plain, "RETURNED") {
		t.Errorf("%d returned agents off the tree with nothing listing them:\n%s", n, plain)
	}
}

// The selected agent is never degraded below activity + station (§3.17).
func TestSelectedKeepsActivityLine(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	frame, _ := renderFrame(w, v, 64, 24, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, nowMark+" blocked · asked to clear the TS cache") {
		t.Errorf("selected ask agent lost its activity line at 24 rows:\n%s", plain)
	}
}
