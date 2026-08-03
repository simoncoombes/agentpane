package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/narrate"
	"github.com/simoncoombes/agentpane/internal/state"
)

// The three §13.2 scenes, built from the demo world the way the mock's own
// units() does: the blocked scene as shipped, the ask answered, then everything
// back. Using the real demo world means the geometry test is measured against
// the tree the pane actually draws.
func voiceScenes(t *testing.T) map[string]func() (*UIState, state.World) {
	t.Helper()
	m, events := demoMachine(t)
	return map[string]func() (*UIState, state.World){
		"blocked": func() (*UIState, state.World) { return demoView(t, m, events) },
		"risky": func() (*UIState, state.World) {
			v, w := demoView(t, m, events)
			w.Asks = nil
			agents := append([]state.Agent(nil), w.Agents...)
			for i := range agents {
				if agents[i].Status == state.StatusAsk {
					agents[i].Status = state.StatusRun
					agents[i].Ask = nil
				}
			}
			w.Agents = agents
			return v, w
		},
		"clear": func() (*UIState, state.World) {
			v, w := demoView(t, m, events)
			w.Asks = nil
			w.Contentions = nil
			agents := append([]state.Agent(nil), w.Agents...)
			for i := range agents {
				if agents[i].Teammate {
					continue
				}
				agents[i].Status = state.StatusDone
				agents[i].Station = 3
				agents[i].OpenCall = nil
				if agents[i].DoneAt.IsZero() {
					agents[i].DoneAt = demoNow().Add(-time.Duration(i) * time.Second)
				}
			}
			w.Agents = agents
			v.SelID = event.MainAgentID
			return v, w
		},
	}
}

// treeTop is the y of the tree's first row: the first line the frame attributes
// to main. It is read from the hit-test metadata rather than by matching text,
// so it measures the geometry and not the copy.
func treeTop(meta *frameMeta) int {
	for y, id := range meta.lineIDs {
		if id == event.MainAgentID {
			return y
		}
	}
	return -1
}

// longSentence is the §13.4 stress case: a single 200-character standup sentence.
// It is injected by giving an agent a very long name, which is what the narrator
// interpolates, so the length arrives through the real template path.
const longSentence = "a task description written by somebody who bills by the word and has never once " +
	"considered that it might have to fit inside a terminal pane that is only sixty four columns wide"

// §13.4, the headline invariant: the tree's first row never changes y-position
// when the scene, the voice mode, or any sentence length changes.
//
// It holds by construction — narration is drawn BELOW the tree — and this test is
// what keeps it that way: move the region above the tree and every cell of this
// matrix fails at once.
func TestTreeTopIsInvariantAcrossVoiceScenesAndSentenceLength(t *testing.T) {
	for name, build := range voiceScenes(t) {
		var want = -1
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
			for _, long := range []bool{false, true} {
				v, w := build()
				v.Voice = mode
				if long {
					agents := append([]state.Agent(nil), w.Agents...)
					agents[0].Name = longSentence
					agents[0].NameExact = true
					w.Agents = agents
				}
				v.advanceNarration(w, demoNow(), 64)
				_, meta := renderFrame(w, v, 64, 72, demoNow(), NewPalette(2, false))
				got := treeTop(meta)
				if got < 0 {
					t.Fatalf("%s/%s/long=%v: no tree row in the frame", name, mode, long)
				}
				if want < 0 {
					want = got
					continue
				}
				if got != want {
					t.Errorf("%s: tree top moved to y=%d (was %d) at voice=%s long=%v",
						name, got, want, mode, long)
				}
			}
		}
	}
}

// The other half of the same guarantee: the region's height is a pure function of
// the mode. A region that measured its content and asked for room is exactly the
// bug §13.4 forbids.
func TestNarrationHeightIsFixedPerMode(t *testing.T) {
	for name, build := range voiceScenes(t) {
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
			for _, width := range []int{64, 44} {
				for _, long := range []bool{false, true} {
					v, w := build()
					v.Voice = mode
					if long {
						agents := append([]state.Agent(nil), w.Agents...)
						agents[0].Name = longSentence
						w.Agents = agents
					}
					v.advanceNarration(w, demoNow(), width)
					lines := renderNarration(w, v, mode, width, demoNow(), NewPalette(2, false))
					if len(lines) != narrationHeight(mode) {
						t.Errorf("%s/%s/%d/long=%v: %d lines, want %d",
							name, mode, width, long, len(lines), narrationHeight(mode))
					}
					for i, ln := range lines {
						if segsWidth(ln) > width {
							t.Errorf("%s/%s/%d: narration line %d is %d wide",
								name, mode, width, i, segsWidth(ln))
						}
					}
				}
			}
		}
	}
}

// The whole frame still fits the pane at every voice mode and every geometry:
// narration may never push the footer off the bottom.
func TestNarrationNeverOverflowsTheFrame(t *testing.T) {
	m, events := demoMachine(t)
	for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
		for _, rows := range []int{12, 16, 24, 44, 60, 72, 90} {
			for _, cols := range []int{38, 44, 64, 100} {
				v, w := demoView(t, m, events)
				v.Voice = mode
				v.advanceNarration(w, demoNow(), frameWidth(v, cols))
				frame, _ := renderFrame(w, v, cols, rows, demoNow(), NewPalette(2, false))
				got := strings.Split(frame, "\n")
				if len(got) != rows {
					t.Errorf("voice=%s %dx%d produced %d lines", mode, cols, rows, len(got))
				}
			}
		}
	}
}

// The geometry rule, stated as a test: the tree wins. Narration is withheld
// wherever reserving it would cost the tree a row or an activity line, and it
// appears as soon as the pane can afford it.
func TestTreeWinsWhenThereIsNoRoomForBoth(t *testing.T) {
	m, events := demoMachine(t)
	drawn := func(rows int) VoiceMode {
		v, w := demoView(t, m, events)
		v.Voice = VoiceFull
		leftover := narrationLeftover(w, v, 64, rows, demoNow(), NewPalette(2, false))
		mode, _ := narrationPlan(w, v, 64, leftover, demoNow(), NewPalette(2, false))
		return mode
	}
	// The demo's tree needs the whole of a 44-row pane, so the voice yields.
	if got := drawn(44); got != VoiceOff {
		t.Errorf("44 rows: voice=%s, want off — the tree needs every row", got)
	}
	// Grow the pane and the regions come back, cheapest first.
	if got := drawn(60); got != VoiceStandup {
		t.Errorf("60 rows: voice=%s, want standup", got)
	}
	if got := drawn(72); got != VoiceFull {
		t.Errorf("72 rows: voice=%s, want full", got)
	}
	// And the ladder only ever loses regions — it never skips from full to off
	// while the standup would have fitted.
	for rows := 30; rows <= 90; rows++ {
		mode := drawn(rows)
		if mode == VoiceOff {
			continue
		}
		if mode == VoiceFull && drawn(rows-1) == VoiceOff && rows > 30 {
			t.Errorf("rows=%d jumped from off straight to full", rows)
		}
	}
}

// Whatever the voice is doing, the tree never loses a row or an activity line to
// it. This is the substance of "the tree wins" — the mode is allowed to cost
// tool-call history and nothing else.
func TestNarrationNeverCostsTheTreeARowOrAnActivityLine(t *testing.T) {
	m, events := demoMachine(t)
	for _, rows := range []int{24, 44, 60, 72, 90} {
		var base int
		for _, mode := range []VoiceMode{VoiceOff, VoiceStandup, VoiceFull} {
			v, w := demoView(t, m, events)
			v.Voice = mode
			v.advanceNarration(w, demoNow(), 64)
			frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
			plain := stripANSI(frame)
			// One "⤷" per activity line, one row head per agent.
			acts := strings.Count(plain, "⤷")
			if mode == VoiceOff {
				base = acts
				continue
			}
			if acts != base {
				t.Errorf("rows=%d voice=%s: %d activity lines, %d with the voice off",
					rows, mode, acts, base)
			}
		}
	}
}

// Auto-follow: at rest the newest entry is on the last body row, and new entries
// keep arriving there.
func TestCommentaryAutoFollowsTheNewestEntry(t *testing.T) {
	v, _ := commentaryFixture(t, 40)
	if v.CommScroll != 0 {
		t.Fatalf("fixture starts scrolled: %d", v.CommScroll)
	}
	last := v.Narr.Entries[len(v.Narr.Entries)-1]
	lines := renderCommentary(v, 64, NewPalette(2, false))
	if !strings.Contains(plainLines(lines), headWords(last.Text)) {
		t.Errorf("newest entry is not visible while following:\n%s", plainLines(lines))
	}
	if strings.Contains(plainLines(lines), "paused") {
		t.Errorf("header claims paused at offset zero")
	}
}

// Pause and resume. Scrolling up pins the view: entries arriving afterwards must
// not slide it, and paging back to the bottom resumes following.
func TestCommentaryPausesOnScrollUpAndResumesAtTheBottom(t *testing.T) {
	m := commentaryModel(t, 72)
	pal := NewPalette(2, false)

	m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.v.CommScroll == 0 {
		t.Fatal("⇞ did not scroll up")
	}
	pinned := commentaryBody(renderCommentary(m.v, 64, pal))
	newest := m.v.Narr.Entries[len(m.v.Narr.Entries)-1]
	if strings.Contains(pinned, headWords(newest.Text)) {
		t.Errorf("still showing the newest entry after scrolling up:\n%s", pinned)
	}

	// Ten more entries arrive. The pinned view must hold still.
	feedCommentary(m, 10)
	if held := commentaryBody(renderCommentary(m.v, 64, pal)); held != pinned {
		t.Errorf("paused view slid when entries arrived:\n--- was ---\n%s\n--- now ---\n%s",
			pinned, held)
	}

	// Page back down until the bottom; follow resumes there and nowhere else.
	for i := 0; i < 40 && m.v.CommScroll > 0; i++ {
		m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.v.CommScroll != 0 {
		t.Fatalf("paging down never reached the bottom: %d", m.v.CommScroll)
	}
	back := plainLines(renderCommentary(m.v, 64, pal))
	newest = m.v.Narr.Entries[len(m.v.Narr.Entries)-1]
	if !strings.Contains(back, headWords(newest.Text)) {
		t.Errorf("follow did not resume at the bottom:\n%s", back)
	}
}

// ⇞/⇟ paging: bounded at both ends, and a page is a page (less one line of
// overlap) rather than a single row.
func TestCommentaryPagingIsBoundedAndPagesAPage(t *testing.T) {
	m := commentaryModel(t, 72)
	total := commentaryTotalLines(m.v.Narr.Entries, 64)
	max := total - commentaryBodyRows()

	m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.v.CommScroll != commentaryPage() {
		t.Errorf("one ⇞ moved %d lines, want %d", m.v.CommScroll, commentaryPage())
	}
	for i := 0; i < 100; i++ {
		m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if m.v.CommScroll != max {
		t.Errorf("⇞ ran past the top: %d, want %d", m.v.CommScroll, max)
	}
	for i := 0; i < 100; i++ {
		m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.v.CommScroll != 0 {
		t.Errorf("⇟ ran past the bottom: %d", m.v.CommScroll)
	}
}

// With the commentary hidden the page keys move the standup body instead — the
// only scrollable region left — and they still never touch the tree.
func TestPageKeysMoveTheStandupWhenTheCommentaryIsHidden(t *testing.T) {
	m := commentaryModel(t, 60)
	m.v.Voice = VoiceStandup
	if m.voiceDrawn() != VoiceStandup {
		t.Fatalf("standup not drawn at %dx%d", m.cols, m.rows)
	}
	before := m.v.SelID
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.v.StandupScroll == 0 {
		t.Errorf("⇟ did not scroll the standup body")
	}
	if m.v.CommScroll != 0 {
		t.Errorf("⇟ moved the hidden commentary: %d", m.v.CommScroll)
	}
	if m.v.SelID != before {
		t.Errorf("⇟ moved the tree selection to %s", m.v.SelID)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.v.StandupScroll != 0 {
		t.Errorf("⇞ did not return the standup to the top: %d", m.v.StandupScroll)
	}
}

// With the inspector open the page keys belong to it, including the
// suppressed-agents census (§13.3 Q8's handoff).
func TestPageKeysBelongToTheInspectorWhenItIsOpen(t *testing.T) {
	m := commentaryModel(t, 72)
	m.v.SelID = selQuiet
	m.v.InspectorOpen = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.v.InspectorScroll != inspectorMax-5 {
		t.Errorf("⇞ moved the inspector %d rows, want %d", m.v.InspectorScroll, inspectorMax-5)
	}
	if m.v.CommScroll != 0 {
		t.Errorf("⇞ also moved the commentary: %d", m.v.CommScroll)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.v.InspectorScroll != 0 {
		t.Errorf("⇟ ran past the bottom of the inspector: %d", m.v.InspectorScroll)
	}
}

// j/k stay bound to the tree at all times (§13.2): they move the selection and
// they never touch either narration scroll, at any voice mode.
func TestSelectionKeysAreUnaffectedByNarration(t *testing.T) {
	for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
		m := commentaryModel(t, 72)
		m.v.Voice = mode
		m.v.CommScroll = 3
		m.v.StandupScroll = 2
		start := m.v.SelID
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if m.v.SelID == start {
			t.Errorf("voice=%s: j did not move the selection", mode)
		}
		if m.v.CommScroll != 3 || m.v.StandupScroll != 2 {
			t.Errorf("voice=%s: j moved a narration scroll (%d, %d)",
				mode, m.v.CommScroll, m.v.StandupScroll)
		}
		down := m.v.SelID
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		if m.v.SelID != start {
			t.Errorf("voice=%s: k did not come back to %s (at %s)", mode, start, down)
		}
	}
}

// `n` cycles standup + commentary → standup → off, and back.
func TestVoiceKeyCycles(t *testing.T) {
	m := commentaryModel(t, 72)
	want := []VoiceMode{VoiceStandup, VoiceOff, VoiceFull, VoiceStandup}
	for i, w := range want {
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		if m.v.Voice != w {
			t.Fatalf("press %d: voice=%s, want %s", i+1, m.v.Voice, w)
		}
		if !strings.Contains(m.v.Toast.Text, w.label()) {
			t.Errorf("press %d: toast %q does not name %s", i+1, m.v.Toast.Text, w.label())
		}
	}
}

// A mode the pane has no room for is still selected — the request survives so it
// reappears when the pane grows — but the toast says it is not on screen rather
// than letting the key look broken.
func TestVoiceKeySaysWhenThereIsNoRoom(t *testing.T) {
	m := commentaryModel(t, 44) // the demo's tree needs all of it
	m.v.Voice = VoiceOff
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.v.Voice != VoiceFull {
		t.Fatalf("voice=%s", m.v.Voice)
	}
	if !strings.Contains(m.v.Toast.Text, "no room") {
		t.Errorf("toast %q does not admit the region was withheld", m.v.Toast.Text)
	}
}

// Honest absence: a run with nothing to say still draws both regions, at their
// full height, and says why they are empty.
func TestNarrationHonestAbsence(t *testing.T) {
	v := newUIState(testConfig())
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Connected: true,
	}
	v.advanceNarration(w, demoNow(), 64)
	lines := renderNarration(w, v, VoiceFull, 64, demoNow(), NewPalette(2, false))
	if len(lines) != narrationHeight(VoiceFull) {
		t.Fatalf("%d lines, want %d", len(lines), narrationHeight(VoiceFull))
	}
	plain := plainLines(lines)
	if !strings.Contains(plain, copyNoCommentary) {
		t.Errorf("empty commentary does not say so:\n%s", plain)
	}
	if !strings.Contains(plain, "Nothing's reported yet") {
		t.Errorf("empty standup does not say the run has reported nothing:\n%s", plain)
	}
	if !strings.Contains(plain, "nothing needs you") {
		t.Errorf("empty standup does not close on an action:\n%s", plain)
	}
}

// §13.1(a): a name that has appeared in the standup is frozen. The late exact
// description is recorded and shows in the inspector, and nowhere else.
func TestNarrationFreezesTheNamesItSpeaks(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)

	spoken := ""
	for _, a := range w.Agents {
		if a.Status == state.StatusAsk {
			spoken = a.ID
		}
	}
	if spoken == "" {
		t.Fatal("no ask agent in the demo world")
	}

	// Narrating alone must NOT freeze anything: with the voice off, or at a
	// geometry where the region is withheld, or for an entry clipped out of the
	// commentary window, nobody has read the name — and freezing it there
	// silently defeats the guess→exact correction §13.1 keeps.
	v.advanceNarration(w, demoNow(), 64)
	if v.Slugs.Published(spoken) {
		t.Fatalf("%s was frozen before any frame drew it", spoken)
	}
	guessed := v.Slugs.Assign(spoken, "")
	if corrected := v.Slugs.AssignExact(spoken, "a late exact description"); corrected == guessed {
		t.Errorf("an unpublished slug must still be correctable: stayed %q", corrected)
	}

	// Drawing the standup is what publishes it; from then on the name holds.
	// The pane must be tall enough that narrationPlan actually keeps the
	// region — at 44 rows the demo tree consumes everything and the plan
	// degrades to VoiceOff, which correctly publishes nothing.
	v2, w2 := demoView(t, m, events)
	v2.advanceNarration(w2, demoNow(), 64)
	renderFrame(w2, v2, 64, 72, demoNow(), NewPalette(2, false))
	if !v2.Slugs.Published(spoken) {
		t.Fatalf("%s was named in a drawn standup but not published", spoken)
	}
	before := v2.Slugs.Assign(spoken, "")
	after := v2.Slugs.AssignExact(spoken, "an entirely different description arriving late")
	if after != before {
		t.Errorf("a published slug renamed: %q → %q", before, after)
	}
	if late := v2.Slugs.LateName(spoken); late == "" {
		t.Errorf("the late name was dropped instead of recorded")
	}
}

// ⏎ on the suppressed-agents line opens the phantom census, not an agent view
// full of dashes (§13.3 Q8).
func TestEnterOnTheQuietLineOpensThePhantomView(t *testing.T) {
	m := commentaryModel(t, 44)
	if len(m.world.Quiet) == 0 {
		t.Skip("the demo world holds no suppressed agents")
	}
	m.v.SelID = selQuiet
	m.v.Follow = true
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.v.InspectorOpen {
		t.Fatal("⏎ did not open the inspector")
	}
	if m.v.Follow {
		t.Errorf("follow survived on a view with nothing to follow")
	}
	lines := renderInspector(m.world, m.v, 64, m.clock, m.pal)
	plain := plainLines(lines)
	if !strings.Contains(plain, "suppressed agents") {
		t.Errorf("⏎ opened the wrong view:\n%s", plain)
	}
	if strings.Contains(plain, "UNKNOWN") {
		t.Errorf("the quiet line still falls through to an agent view:\n%s", plain)
	}
}

// f on the quiet line explains itself instead of silently doing nothing.
func TestFollowOnTheQuietLineSaysThereIsNothingToFollow(t *testing.T) {
	m := commentaryModel(t, 44)
	m.v.SelID = selQuiet
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if m.v.Follow {
		t.Errorf("follow engaged on the suppressed-agents line")
	}
	if !strings.Contains(m.v.Toast.Text, "nothing to follow") {
		t.Errorf("toast %q does not explain the no-op", m.v.Toast.Text)
	}
}

// --- fixtures ---

// commentaryFixture is the demo world with a synthetic commentary long enough to
// scroll: the demo's own history is about eighteen entries, which is only a
// couple of pages.
func commentaryFixture(t *testing.T, extra int) (*UIState, state.World) {
	t.Helper()
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.Voice = VoiceFull
	v.advanceNarration(w, demoNow(), 64)
	appendSynthetic(v, extra)
	return v, w
}

func commentaryModel(t *testing.T, rows int) *Model {
	t.Helper()
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.Voice = VoiceFull
	v.advanceNarration(w, demoNow(), 64)
	appendSynthetic(v, 40)

	mm := &Model{
		cfg:   Config{},
		v:     v,
		pal:   NewPalette(2, false),
		world: w,
		clock: demoNow(),
		cols:  64,
		rows:  rows,
	}
	return mm
}

// feedCommentary appends entries the way advanceNarration does, including the
// paused-scroll adjustment, so the pause test exercises the real path.
func feedCommentary(m *Model, n int) {
	fresh := syntheticEntries(len(m.v.Narr.Entries), n)
	m.v.Narr.Entries = append(m.v.Narr.Entries, fresh...)
	if m.v.CommScroll > 0 {
		m.v.CommScroll += commentaryTotalLines(fresh, frameWidth(m.v, m.cols))
	}
}

func appendSynthetic(v *UIState, n int) {
	v.Narr.Entries = append(v.Narr.Entries, syntheticEntries(len(v.Narr.Entries), n)...)
}

func syntheticEntries(from, n int) []narrate.Entry {
	out := make([]narrate.Entry, 0, n)
	for i := 0; i < n; i++ {
		k := from + i
		out = append(out, narrate.Entry{
			Key:  "synthetic:" + itoa(k),
			At:   time.Duration(300+k*7) * time.Second,
			Tone: narrate.ToneDim,
			Text: "Synthetic entry number " + itoa(k) + " exists so the commentary is long enough " +
				"to page through, and it is deliberately wordy so that it wraps.",
		})
	}
	return out
}

func plainLines(lines [][]seg) string {
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(stripANSI(renderSegs(ln)))
		b.WriteByte('\n')
	}
	return b.String()
}

// headWords is the needle for "is this entry on screen": the opening of its text,
// which sits on the entry's own first line next to its timestamp and is unique
// per entry (a tail is not — several entries can end the same way).
func headWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 4 {
		f = f[:4]
	}
	return strings.Join(f, " ")
}

// commentaryBody drops the rule and the header, so a comparison is about the
// entries on screen and not about the live entry COUNT in the header — which is
// meant to change when entries arrive, even while the view is pinned.
func commentaryBody(lines [][]seg) string {
	if len(lines) > 2 {
		return plainLines(lines[2:])
	}
	return ""
}

// --- goldens ---
//
// The narration goldens use a TALL pane on purpose. On the shipped 44-row pane the
// demo's tree needs every row and narrationPlan withholds the region entirely
// (TestTreeWinsWhenThereIsNoRoomForBoth pins that), so a 44-row golden would
// record an empty feature. 72 rows is a full-height iTerm2 pane at a normal font
// size, which is where the region actually lives.

func TestSnapshotNarration64(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.advanceNarration(w, demoNow(), 64)
	frame, _ := renderFrame(w, v, 64, 72, demoNow(), NewPalette(2, false))
	checkGolden(t, "narration_64", frame)
}

func TestSnapshotNarration44(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.WidthMode = "narrow"
	v.advanceNarration(w, demoNow(), 44)
	frame, _ := renderFrame(w, v, 44, 72, demoNow(), NewPalette(2, false))
	checkGolden(t, "narration_44", frame)
}

// The standup on its own, with the commentary traded away for four more rows.
func TestSnapshotNarrationStandupOnly64(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.Voice = VoiceStandup
	v.advanceNarration(w, demoNow(), 64)
	frame, _ := renderFrame(w, v, 64, 72, demoNow(), NewPalette(2, false))
	checkGolden(t, "narration_standup_64", frame)
}

// With the voice off nothing is published, however many frames are drawn: the
// §13.1(a) freeze is about a name a reader could have READ in prose. The tree
// deliberately does not publish — a row shows the slug on the very first frame,
// so treating the tree as a publisher would make the guess→exact correction
// §13.1 explicitly keeps impossible to ever apply.
func TestVoiceOffNeverFreezesAName(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.Voice = VoiceOff
	v.advanceNarration(w, demoNow(), 64)
	for _, rows := range []int{20, 44, 72} {
		renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
	}
	for _, a := range w.Agents {
		if v.Slugs.Published(a.ID) {
			t.Fatalf("%s was frozen with the voice off", a.ID)
		}
	}
	// And a name drawn only in the tree stays correctable.
	var id, guessed string
	for _, a := range w.Agents {
		if a.Status == state.StatusAsk {
			id, guessed = a.ID, v.Slugs.Assign(a.ID, "")
		}
	}
	if id == "" {
		t.Fatal("no ask agent in the demo world")
	}
	if got := v.Slugs.AssignExact(id, "a late exact description"); got == guessed {
		t.Errorf("a tree-only name must stay correctable: stayed %q", got)
	}
}
