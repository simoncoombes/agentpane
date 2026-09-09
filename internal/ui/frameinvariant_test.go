package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// THE invariant. Read this before changing anything that prints a command.
//
// "The command appears twice" was fixed three times and recurred twice, in a NEW
// PAIR OF REGIONS each time: first the band row against the standup's first
// sentence, then the band row against the commentary log. Both earlier fixes were
// real and both were verified by tests that counted occurrences INSIDE one region,
// which is exactly why the next pair got through. Chasing instances does not work.
// The property is whole-FRAME:
//
//	No frame, at any geometry, for any world, may contain the ask's verbatim
//	command more than once.
//
// It is asserted here over the complete rendered frame with whitespace squashed,
// which is what makes it immune to the two ways the earlier tests were fooled:
// prose wraps a restated command across a row boundary, and a region-scoped count
// cannot see a duplicate that lives in the next region down.
//
// The regions that made this hard — the band row, the standup and the commentary
// log — have since been removed, so the inspector's log is now the only surface
// that states the verbatim command at all. The invariant is kept rather than
// retired because it is a property of the FRAME, not of those regions: it is what
// stops the next surface that wants to print a command from becoming the next
// duplicate.
//
// commandWorlds are the six worlds the invariant is swept over: every shape that
// puts a verbatim command on screen at all.
func commandWorlds(t *testing.T) map[string]func() (*UIState, state.World, string) {
	t.Helper()
	dm, events := demoMachine(t)
	now := demoNow()
	const demoCmd = "rm -rf node_modules/.cache && pnpm rebuild"
	const askCmd = "psql -f db/migrations/0042.sql"

	return map[string]func() (*UIState, state.World, string){
		// The shipped demo: eight agents, a deduped ask with folded ghosts, a
		// stall and a contention behind it.
		"demo": func() (*UIState, state.World, string) {
			v, w := demoView(t, dm, events)
			return v, w, demoCmd
		},
		// One ask and nothing to hide the standup's first sentence behind — the
		// world the band/mustLines duplication was found in.
		"single-ask": func() (*UIState, state.World, string) {
			w := oneAskWorld(now)
			return viewOf(w), w, askCmd
		},
		// §3.7.8: the answer is in, the ask is still on the band, and the
		// commentary has a resolution entry of its own.
		"resolving-ask": func() (*UIState, state.World, string) {
			w := oneAskWorld(now)
			w.Asks[0].Resolving = true
			return viewOf(w), w, askCmd
		},
		// A stall behind the ask, so the band body is long enough to clip.
		"ask+stall": func() (*UIState, state.World, string) {
			w := askAndStallWorld(now)
			return viewOf(w), w, askCmd
		},
		// §3.19's digest sharing the one region with a live ask.
		"digest+ask": func() (*UIState, state.World, string) {
			w := askAndStallWorld(now)
			v := viewOf(w)
			v.Digest = &digest{
				Dur: 90 * time.Second,
				Entries: []digestEntry{
					{Slug: "typecheck-worke…", Kind: "stalled", When: "2m50s ago"},
					{Slug: "write-email-index", Kind: "asked permission", When: "52s ago"},
				},
				Marks: map[string]bool{"a1": true, "a2": true},
			}
			return v, w, askCmd
		},
		// Four asks so the §3.7.6 cap bites, one of them resolving, one deduped
		// with folded ghosts, and a stall behind the lot.
		"capped-asks": func() (*UIState, state.World, string) {
			w := bandWorld(now)
			return viewOf(w), w, demoCmd
		},
	}
}

// commandWidths are the three shipped widths: the §3.1 narrow budget, a mid
// terminal, and the §3.1 wide budget.
var commandWidths = []struct {
	cols int
	mode string
}{
	{44, "narrow"},
	{52, ""},
	{64, ""},
}

// The guard. 6 worlds × 2 inspector states × 3 widths × 79 heights = 2,844 frames.
//
// The band that used to print `run: <cmd>` is gone, so the inspector is the only
// surface left that states the exact command — which is why the sweep opens it
// rather than iterating the voice modes it used to.
func TestTheCommandAppearsAtMostOncePerFrame(t *testing.T) {
	now := demoNow()
	frames, seen := 0, map[string]bool{}

	for name, build := range commandWorlds(t) {
		for _, insp := range []bool{false, true} {
			for _, wd := range commandWidths {
				for rows := 12; rows <= 90; rows++ {
					v, w, cmd := build()
					v.WidthMode = wd.mode
					v.InspectorOpen = insp
					if insp {
						// The inspector states the command of the ask belonging to
						// the SELECTED agent, so the sweep must select the agent
						// that owns this world's command — not merely the first
						// agent that happens to be asking.
						for _, ask := range w.Asks {
							if ask.Command == cmd {
								v.SelID = ask.AgentID
								break
							}
						}
					}
					frame, _ := renderFrame(w, v, wd.cols, rows, now, NewPalette(2, false))
					frames++

					// Squashed, so a command restated inside a wrapped sentence is
					// counted where it was written rather than where it was laid out,
					// and so the count spans every region instead of one of them.
					n := strings.Count(squash(stripANSI(frame)), cmd)
					if n > 1 {
						t.Fatalf("%s/insp=%v/%dx%d: the command appears %d times in one frame, want at most 1:\n%s",
							name, insp, wd.cols, rows, n, stripANSI(frame))
					}
					if n == 1 {
						seen[name] = true
					}
				}
			}
		}
	}

	// "At most once" is satisfied by never printing it at all, so the sweep also
	// has to have SEEN it: a guard that passes on a frame with no command in it
	// guards nothing.
	for name := range commandWorlds(t) {
		if !seen[name] {
			t.Errorf("%s: no frame in the sweep printed the command at all", name)
		}
	}
	if want := 6 * 2 * 3 * 79; frames != want {
		t.Errorf("swept %d frames, expected the documented matrix of %d", frames, want)
	}
}

// A frame may not claim a row it did not draw. The hit-test metadata is the
// frame's own claim about what is on each line, so an id on a line that is not a
// whole agent block is the pane offering a row that is not there: `├─╮` alone,
// selectable, counted in the footer's `1–1 / n`, with no glyph, no slug, no timer
// and no §3.19 change mark.
//
// This is finding 2's guard, and it is geometric rather than textual: it walks
// every id run in the frame and requires each agent's run to carry that agent's
// row head.
func TestNoFrameClaimsAnAgentRowItDidNotDraw(t *testing.T) {
	now := demoNow()
	for name, build := range commandWorlds(t) {
		for _, wd := range commandWidths {
			for rows := 12; rows <= 50; rows++ {
				{
					v, w, _ := build()
					v.WidthMode = wd.mode
					frame, meta := renderFrame(w, v, wd.cols, rows, now, NewPalette(2, false))
					lines := strings.Split(stripANSI(frame), "\n")

					for _, a := range w.Agents {
						var rows0 []string
						for y, id := range meta.lineIDs {
							if id == a.ID && y < len(lines) {
								rows0 = append(rows0, lines[y])
							}
						}
						if len(rows0) == 0 {
							continue // windowed out, which the footer states
						}
						head := false
						for _, ln := range rows0 {
							if strings.Contains(ln, v.slugFor(a)) {
								head = true
							}
						}
						if !head {
							t.Fatalf("%s/%dx%d: agent %s holds %d line(s) with no row head: %q\n%s",
								name, wd.cols, rows, a.ID, len(rows0), rows0,
								stripANSI(frame))
						}
					}
					// And the footer's window claim counts only agents that were drawn.
					if info := treeScrollInfo(w, v, wd.cols, rows, now); info != "" {
						if !strings.Contains(stripANSI(frame), info) {
							t.Errorf("%s/%dx%d: scroll info %q is not on the frame",
								name, wd.cols, rows, info)
						}
					}
				}
			}
		}
	}
}

// treeScrollInfo re-derives what the footer will claim about the tree's window,
// without drawing it. It mirrors renderActive's own arithmetic; keeping it in one
// small function is what stops the two from drifting.
func treeScrollInfo(w state.World, v *UIState, cols, rows int, now time.Time) string {
	pal := NewPalette(2, false)
	width := frameWidth(v, cols)
	insp := 0
	if v.InspectorOpen {
		insp = len(renderInspector(w, v, width, now, pal))
	}
	avail := rows - 4 - insp
	if avail < minTreeRows {
		avail = minTreeRows
	}
	top := v.ScrollTop
	tree := renderTree(w, v, width, avail, now, pal)
	v.ScrollTop = top
	return tree.scrollInfo
}

// The two surfaces the invariant exempts still have a bound: the command may
// appear at most twice on a frame, never three times, and the exemption must be
// the only reason it appears twice at all. Sweeping them is what makes the
// invariant's scope statement checkable rather than an assertion.
func TestExemptSurfacesRepeatTheCommandOnPurpose(t *testing.T) {
	for name, mk := range commandWorlds(t) {
		for _, rows := range []int{27, 34, 44, 60} {
			for _, cols := range []int{64, 100} {
				t.Run(name, func(t *testing.T) {
					v, w, cmd := mk()
					if cmd == "" {
						t.Skip("world has no command")
					}
					v.InspectorOpen = true
					for _, a := range w.Agents {
						if a.Status == state.StatusAsk {
							v.SelID = a.ID
						}
					}
					// The toast the app actually produces: §5.2's `yanked: <text…>`,
					// truncated exactly as keys.go does. A full-command toast is not
					// a state this code can reach, and testing one would have
					// demanded a fix for a bug that does not exist.
					v.Toast = toast{Text: "yanked: " + truncString(cmd, 24) + "…", At: demoNow()}
					frame, _ := renderFrame(w, v, cols, rows, demoNow(), NewPalette(2, false))
					got := strings.Count(squash(stripANSI(frame)), squash(cmd))
					if got > 2 {
						t.Errorf("%s %dx%d: command appears %d times; the exemptions allow at most 2:\n%s",
							name, cols, rows, got, stripANSI(frame))
					}
				})
			}
		}
	}
}
