package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/narrate"
	"github.com/simoncoombes/agentpane/internal/state"
)

// THE invariant. Read this before changing anything that prints a command.
//
// "The command appears twice" has now been fixed three times and recurred twice,
// in a NEW PAIR OF REGIONS each time: first the band row against the standup's
// first sentence, then the band row against the commentary log. Both earlier
// fixes were real and both were verified by tests that counted occurrences
// INSIDE one region — §3.7's — which is exactly why the next pair got through.
// Chasing instances does not work. The property is whole-FRAME:
//
//	No frame, at any geometry, in any voice mode, for any world, may contain the
//	ask's verbatim command more than once.
//
// It is asserted here over the complete rendered frame with whitespace squashed,
// which is what makes it immune to the two ways the earlier tests were fooled:
// prose wraps a restated command across a row boundary, and a region-scoped count
// cannot see a duplicate that lives in the next region down.
//
// THE RULING the code implements to satisfy it (narrate.Entry.Cmd holds the
// canonical statement of it):
//
//	The verbatim command belongs to the band row, because §3.7 requires it there
//	— that row is what the reader approves. The commentary is a LOG: it may keep
//	the exact bytes for the historical record, but not while the same command is
//	on screen in the band. So the commentary paraphrases (naming the kind, as
//	askGist does) while the ask is live and the band is drawn, and records the
//	verbatim command once the ask has RESOLVED, when the band no longer shows it.
//
// That keeps the log's lasting record of the approved bytes — nothing is
// rewritten, both forms are fixed when the entry is appended — without ever
// printing the same command twice in one frame.

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

// The guard. 6 worlds × 3 voice modes × 3 widths × 79 heights = 4,266 frames.
func TestTheCommandAppearsAtMostOncePerFrame(t *testing.T) {
	now := demoNow()
	modes := []VoiceMode{VoiceFull, VoiceStandup, VoiceOff}
	frames, seen := 0, map[string]bool{}

	for name, build := range commandWorlds(t) {
		for _, mode := range modes {
			for _, wd := range commandWidths {
				for rows := 12; rows <= 90; rows++ {
					v, w, cmd := build()
					v.Voice = mode
					v.WidthMode = wd.mode
					v.advanceNarration(w, now, frameWidth(v, wd.cols))
					frame, _ := renderFrame(w, v, wd.cols, rows, now, NewPalette(2, false))
					frames++

					// Squashed, so a command restated inside a wrapped sentence is
					// counted where it was written rather than where it was laid out,
					// and so the count spans every region instead of one of them.
					n := strings.Count(squash(stripANSI(frame)), cmd)
					if n > 1 {
						t.Fatalf("%s/%s/%dx%d: the command appears %d times in one frame, want at most 1:\n%s",
							name, mode, wd.cols, rows, n, stripANSI(frame))
					}
					if n == 1 {
						seen[name] = true
					}
				}
			}
		}
	}

	// "At most once" is satisfied by never printing it at all, so the sweep also
	// has to have SEEN it: §3.7 requires the exact command on the band's row, and
	// a guard that passes on a frame with no command in it guards nothing.
	for name := range commandWorlds(t) {
		if !seen[name] {
			t.Errorf("%s: no frame in the sweep printed the command at all", name)
		}
	}
	if want := 6 * 3 * 3 * 79; frames != want {
		t.Errorf("swept %d frames, expected the documented matrix of %d", frames, want)
	}
}

// The other half of the ruling: the log is a LOG. Once the ask is gone from the
// band, the commentary's entry for it states the approved bytes — that record is
// the only place they survive, and it is what keeps stripQuoted's job real.
//
// The bytes are never dropped from the Memory: the same entry carries both forms
// from the moment it is appended (narrate.Entry.Text and .Gist), and only which
// one is PRINTED depends on whether the band is showing them. Nothing is
// rewritten, which is the one thing a log may not do.
func TestTheLogRecordsTheApprovedBytesOnceTheAskIsGone(t *testing.T) {
	now := demoNow()
	const cmd = "psql -f db/migrations/0042.sql"
	w := oneAskWorld(now)
	v := viewOf(w)
	v.Voice = VoiceFull
	v.advanceNarration(w, now, 64)

	// While the ask is live: the band has the bytes, the log has the kind.
	live := squash(stripANSI(mustRender(w, v, 64, 60, now)))
	if n := strings.Count(live, cmd); n != 1 {
		t.Fatalf("live ask: the command appears %d times, want exactly 1 (the band row):\n%s", n, live)
	}
	if !strings.Contains(live, "run: "+cmd) {
		t.Errorf("live ask: the band's own row lost the exact command:\n%s", live)
	}
	if !strings.Contains(live, "asked for permission to run one Bash command") {
		t.Errorf("live ask: the log does not name the kind of ask:\n%s", live)
	}
	// The Memory kept the bytes all along; it is the frame that chose the gist.
	recorded := false
	for _, e := range v.Narr.Entries {
		if strings.Contains(e.Text, cmd) {
			recorded = true
			if e.Cmd == "" || e.Gist == "" {
				t.Errorf("the entry quoting the command carries no two-form pair: %+v", e)
			}
		}
	}
	if !recorded {
		t.Errorf("the log never recorded the approved bytes at all:\n%v", v.Narr.Entries)
	}

	// The ask resolves and leaves the band. Now the log states the bytes, and it
	// is the only place on the frame that does.
	done := w
	done.Asks = nil
	agents := append([]state.Agent(nil), w.Agents...)
	agents[0].Status, agents[0].Ask = state.StatusRun, nil
	done.Agents = agents
	v.advanceNarration(done, now, 64)

	after := squash(stripANSI(mustRender(done, v, 64, 60, now)))
	if n := strings.Count(after, cmd); n != 1 {
		t.Fatalf("resolved ask: the command appears %d times, want exactly 1 (the log):\n%s", n, after)
	}
	if strings.Contains(after, "run: "+cmd) {
		t.Errorf("resolved ask: the band still draws a row for an ask that is gone:\n%s", after)
	}
	if !strings.Contains(after, "asked for permission to run `"+cmd+"`") {
		t.Errorf("resolved ask: the log does not state the approved bytes:\n%s", after)
	}
}

func mustRender(w state.World, v *UIState, cols, rows int, now time.Time) string {
	frame, _ := renderFrame(w, v, cols, rows, now, NewPalette(2, false))
	return frame
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
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
			for _, wd := range commandWidths {
				for rows := 12; rows <= 50; rows++ {
					v, w, _ := build()
					v.Voice = mode
					v.WidthMode = wd.mode
					v.advanceNarration(w, now, frameWidth(v, wd.cols))
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
							t.Fatalf("%s/%s/%dx%d: agent %s holds %d line(s) with no row head: %q\n%s",
								name, mode, wd.cols, rows, a.ID, len(rows0), rows0,
								stripANSI(frame))
						}
					}
					// And the footer's window claim counts only agents that were drawn.
					if info := treeScrollInfo(w, v, wd.cols, rows, now); info != "" {
						if !strings.Contains(stripANSI(frame), info) {
							t.Errorf("%s/%s/%dx%d: scroll info %q is not on the frame",
								name, mode, wd.cols, rows, info)
						}
					}
				}
			}
		}
	}
}

// treeScrollInfo re-derives what the footer will claim about the tree's window,
// without drawing it.
func treeScrollInfo(w state.World, v *UIState, cols, rows int, now time.Time) string {
	top := v.ScrollTop
	plan := narrationPlan(w, v, frameWidth(v, cols),
		narrationLeftover(w, v, cols, rows, now, NewPalette(2, false)), now, NewPalette(2, false))
	v.ScrollTop = top
	return plan.tree.scrollInfo
}

// The band's own numbers agree with each other. The header's count, the chip and
// the action row are three statements about one question — is anything waiting on
// the reader — and a frame in which they disagree is a frame that cannot be
// trusted about the one thing this pane is for.
func TestTheBandsSurfacesAgreeAboutWhetherAnythingNeedsYou(t *testing.T) {
	now := demoNow()
	for name, build := range commandWorlds(t) {
		for _, wd := range commandWidths {
			v, w, _ := build()
			v.WidthMode = wd.mode
			entries := bandEntries(w, v, now)
			v.advanceNarration(w, now, frameWidth(v, wd.cols))
			plain := stripANSI(mustRender(w, v, wd.cols, 60, now))
			where := fmt.Sprintf("%s/%d", name, wd.cols)

			blocked := bandNeedsYou(entries) > 0
			// §3.10.2's chip alternates, so a digest owns it outright; the agreement
			// being tested is between the chip and the entries when the chip is the
			// band's own.
			if v.Digest == nil {
				if got := strings.Contains(plain, copyBandNeedsYou); got != blocked {
					t.Errorf("%s: chip says needs-you=%v, entries say %v:\n%s",
						where, got, blocked, plain)
				}
			}
			// `⚑ n NEEDS YOU` in the header and "nothing needs you" on the action row
			// cannot both be on one frame.
			hdr := strings.Contains(plain, "⚑ "+itoa(bandAlertCount(entries))+" NEEDS YOU")
			if hdr && bandAlertCount(entries) > 0 && strings.Contains(plain, "▸ nothing needs you") {
				t.Errorf("%s: the header flags you and the action row says nothing needs you:\n%s",
					where, plain)
			}
			if strings.Contains(plain, "▸ nothing needs you") && strings.Contains(plain, "waiting on you") {
				t.Errorf("%s: main's row says waiting on you and the action row disagrees:\n%s",
					where, plain)
			}
		}
	}
}

// The action row is never cut mid-clause. It is the point of the whole region
// (§13.2), so a truncation there reads as a broken renderer rather than as a
// state — which is what `▸ nothing needs you — waiting on the event …` did in
// every 44-column frame.
func TestTheActionRowIsNeverTruncated(t *testing.T) {
	now := demoNow()
	for name, build := range commandWorlds(t) {
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup} {
			for _, wd := range commandWidths {
				v, w, _ := build()
				v.Voice = mode
				v.WidthMode = wd.mode
				v.advanceNarration(w, now, frameWidth(v, wd.cols))
				plain := stripANSI(mustRender(w, v, wd.cols, 60, now))
				for _, ln := range strings.Split(plain, "\n") {
					if !strings.HasPrefix(ln, "▸ ") {
						continue
					}
					if strings.HasSuffix(strings.TrimRight(ln, " "), "…") {
						t.Errorf("%s/%s/%d: the action row is cut mid-clause: %q",
							name, mode, wd.cols, ln)
					}
				}
			}
		}
	}
}

// §3.7.8's mark outranks the kind when the row cannot hold both. The kind is
// recoverable from the frame; the resolution state is recoverable from nothing.
func TestResolvingMarkSurvivesTheNarrowRow(t *testing.T) {
	now := demoNow()
	w := oneAskWorld(now)
	w.Asks[0].Resolving = true
	for _, width := range []int{44, 52, 64} {
		v := viewOf(w)
		body := bandBody(bandEntries(w, v, now), nil, width, NewPalette(2, false))
		if len(body) == 0 {
			t.Fatalf("width=%d: no band body", width)
		}
		label := segsText(body[0].segs)
		if !strings.Contains(label, copyResolving) {
			t.Errorf("width=%d: the resolving mark did not survive: %q", width, label)
		}
	}
}

// The §3.7.6 cap and the region's one action row cannot disagree about which
// entry the action belongs to: the copy on it must be the copy of an entry the
// reader can actually see.
func TestTheActionRowBelongsToAnEntryThatWasDrawn(t *testing.T) {
	now := demoNow()
	// Three answered asks and a stall fourth: the stall is behind `+1 more`, so its
	// keys may not be the region's action.
	w := bandWorld(now)
	w.Asks = w.Asks[:3]
	for i := range w.Asks {
		w.Asks[i].Resolving = true
	}
	w.Agents = append(w.Agents[:3:3], w.Agents[4])
	v := viewOf(w)
	entries := bandEntries(w, v, now)
	if owner := bandActionOwner(entries); owner >= bandCap {
		t.Fatalf("the action row belongs to entry %d, outside the drawn window of %d",
			owner, bandCap)
	}
	got := standupText(t, w, v, now)
	if strings.Contains(got, copyStallAction) {
		t.Errorf("the region offers a stall's keys for a stall it never drew:\n%s", got)
	}
	if !strings.Contains(got, copyResolvingAction) {
		t.Errorf("the primary entry did not keep the action row:\n%s", got)
	}
	if !strings.Contains(got, "+1 more") {
		t.Errorf("the §3.7.6 cap no longer says what it hid:\n%s", got)
	}
}

// A RESOLVED ask is not the reader's problem, on any of the three surfaces that
// used to say it was (§3.7.8, §3.10.2).
func TestAResolvedAskClaimsNoneOfTheNeedsYouSurfaces(t *testing.T) {
	now := demoNow()
	w := oneAskWorld(now)
	w.Asks[0].Resolving = true
	v := viewOf(w)
	v.advanceNarration(w, now, 64)
	plain := stripANSI(mustRender(w, v, 64, 40, now))

	for _, banned := range []string{"⚑ 1 NEEDS YOU", copyBandNeedsYou, "waiting on you"} {
		if strings.Contains(plain, banned) {
			t.Errorf("a resolved ask still claims %q:\n%s", banned, plain)
		}
	}
	// It is still an alert: the chip drops to worth-watching rather than vanishing,
	// and the entry keeps its row, its mark and its wait.
	if !strings.Contains(plain, narrate.ChipWatch) {
		t.Errorf("the chip did not fall to worth-watching:\n%s", plain)
	}
	for _, want := range []string{"write-email-index  permission · " + copyResolving, "52s",
		"▸ " + narrate.ActionResolving} {
		if !strings.Contains(plain, want) {
			t.Errorf("a resolved ask lost %q:\n%s", want, plain)
		}
	}
	// And one live ask alongside it puts every surface straight back.
	two := oneAskWorld(now)
	two.Asks[0].Resolving = true
	two.Agents = append(two.Agents, state.Agent{
		ID: "a2", Name: "Audit the users schema", NameExact: true, Status: state.StatusAsk,
		Station: 2, SpawnIndex: 2, SpawnedAt: now.Add(-2 * time.Minute),
		LastEventAt: now.Add(-20 * time.Second),
	})
	two.Asks = append(two.Asks, state.Ask{
		Key: "k2", AgentID: "a2", Description: "Audit the users schema", Tool: "Bash",
		Command: "grep -rn createUser src", Kind: "command",
		RaisedAt: now.Add(-20 * time.Second), DupeCount: 1,
	})
	v2 := viewOf(two)
	v2.advanceNarration(two, now, 64)
	back := stripANSI(mustRender(two, v2, 64, 40, now))
	for _, want := range []string{"⚑ 1 NEEDS YOU", copyBandNeedsYou, "waiting on you"} {
		if !strings.Contains(back, want) {
			t.Errorf("an unanswered ask did not take back %q:\n%s", want, back)
		}
	}
}
