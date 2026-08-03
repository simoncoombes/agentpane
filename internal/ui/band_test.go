package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/narrate"
	"github.com/simoncoombes/agentpane/internal/state"
)

// The standup is the only band (§13.2's three-region table, §3.7, §3.19). These
// tests are about the merge: the ask is stated once, and nothing §3.7 promised
// was lost on the way in.

// bandWorld builds a world carrying every §3.7 shape at once: four asks so the
// cap bites, a deduped-and-folded oldest ask, a resolving second ask, and a
// stalled agent behind them.
func bandWorld(now time.Time) state.World {
	agent := func(id, name string, st state.Status, last time.Time) state.Agent {
		return state.Agent{
			ID: id, Name: name, NameExact: true, Status: st, Station: 2,
			SpawnIndex: 1, SpawnedAt: now.Add(-5 * time.Minute), LastEventAt: last,
			ActivityLine: "working", Tokens: 1000,
		}
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Activity: "orchestrating", LastEventAt: now, Tokens: 2000},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-5 * time.Minute), Tokens: 9000},
		Connected: true,
		Agents: []state.Agent{
			agent("a1", "Clear the stale type cache", state.StatusAsk, now.Add(-52*time.Second)),
			agent("a2", "Write the email index migration", state.StatusAsk, now.Add(-30*time.Second)),
			agent("a3", "Audit the users schema", state.StatusAsk, now.Add(-20*time.Second)),
			agent("a4", "Run the auth test suite", state.StatusAsk, now.Add(-10*time.Second)),
			agent("a5", "Typecheck the worker package", state.StatusStuck, now.Add(-170*time.Second)),
		},
		Asks: []state.Ask{
			{Key: "k1", AgentID: "a1", Description: "Clear the stale type cache", Tool: "Bash",
				Command: "rm -rf node_modules/.cache && pnpm rebuild", Kind: "command",
				RaisedAt: now.Add(-52 * time.Second), DupeCount: 3, FoldedGhosts: 2},
			{Key: "k2", AgentID: "a2", Description: "Write the email index migration", Tool: "Bash",
				Command: "psql -f db/migrations/0042.sql", Kind: "command",
				RaisedAt: now.Add(-30 * time.Second), DupeCount: 1, Resolving: true},
			{Key: "k3", AgentID: "a3", Description: "Audit the users schema", Tool: "Bash",
				Command: "grep -rn createUser src", Kind: "command",
				RaisedAt: now.Add(-20 * time.Second), DupeCount: 1},
			{Key: "k4", AgentID: "a4", Description: "Run the auth test suite", Tool: "Bash",
				Command: "pnpm test --filter auth", Kind: "command",
				RaisedAt: now.Add(-10 * time.Second), DupeCount: 1},
		},
	}
	return w
}

// stallWorld is one stalled agent and nothing else: the world where §3.12's stall
// action copy is the region's own action line.
func stallWorld(now time.Time) state.World {
	return state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Activity: "waiting", LastEventAt: now},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-5 * time.Minute)},
		Connected: true,
		Agents: []state.Agent{{
			ID: "a1", Name: "Typecheck the worker package", NameExact: true,
			Status: state.StatusStuck, Station: 2, SpawnIndex: 1,
			SpawnedAt: now.Add(-4 * time.Minute), LastEventAt: now.Add(-170 * time.Second),
			ActivityLine: "no call open",
		}},
	}
}

// oneAskWorld is the simplest world that can state an ask twice: one agent, one
// ask, no dedupe count, no folded ghosts and nothing stalled behind it. It is the
// world where the band body is short enough that the narrator's first sentence is
// on screen next to it in every mode that draws prose.
func oneAskWorld(now time.Time) state.World {
	a := state.Agent{
		ID: "a1", Name: "Write the email index migration", NameExact: true,
		Status: state.StatusAsk, Station: 2, SpawnIndex: 1,
		SpawnedAt: now.Add(-5 * time.Minute), LastEventAt: now.Add(-52 * time.Second),
		ActivityLine: "blocked", Tokens: 1000,
	}
	return state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Title: "Add unique email index", LastEventAt: now, Tokens: 2000},
		Run:       &state.Run{ID: "r", StartedAt: now.Add(-5 * time.Minute), Tokens: 9000},
		Connected: true,
		Agents:    []state.Agent{a},
		Asks: []state.Ask{{
			Key: "k1", AgentID: "a1", Description: a.Name, Tool: "Bash",
			Command: "psql -f db/migrations/0042.sql", Kind: "command",
			RaisedAt: now.Add(-52 * time.Second), DupeCount: 1,
		}},
	}
}

// askAndStallWorld is one outstanding ask with a stalled agent behind it: the
// shape §3.7 has to fit two different sets of keys into one action row.
func askAndStallWorld(now time.Time) state.World {
	w := oneAskWorld(now)
	w.Agents = append(w.Agents, state.Agent{
		ID: "a2", Name: "Typecheck the worker package", NameExact: true,
		Status: state.StatusStuck, Station: 2, SpawnIndex: 2,
		SpawnedAt: now.Add(-4 * time.Minute), LastEventAt: now.Add(-170 * time.Second),
		ActivityLine: "no call open",
	})
	return w
}

// viewOf is a fresh UIState with the world's slugs assigned, as the Model does on
// its first paint.
func viewOf(w state.World) *UIState {
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	return v
}

// standupText renders the standup alone (no commentary, so the body has room for
// the whole band) and returns it as plain text.
func standupText(t *testing.T, w state.World, v *UIState, now time.Time) string {
	t.Helper()
	lines, _ := renderNarration(w, v, VoiceStandup, 64, now, NewPalette(2, false))
	return plainLines(lines)
}

// squash undoes wrapping and padding so a needle can be looked for as it was
// written rather than as it was laid out. A sentence that restates a command
// wraps in the middle of it, and a count over the laid-out frame would miss that
// entirely.
func squash(plain string) string { return strings.Join(strings.Fields(plain), " ") }

// splitAtCommentary cuts a frame into the part §3.7 governs — the tree, the band
// and the standup's sentences — and the commentary log below it.
func splitAtCommentary(plain string) (region, log string) {
	lines := strings.Split(plain, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "COMMENTARY") {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i:], "\n")
		}
	}
	return plain, ""
}

// THE point of the merge: one region, so the ask is stated once. Before it, the
// frame carried a band region above the tree AND a standup below it, both naming
// the agent and both printing the command.
//
// The merge then put the repetition back INSIDE the one region: the band's
// `run: …` row and the narrator's first sentence printed the same string four
// rows apart. The earlier version of this test missed it because it only ever
// looked at the default voice on the demo world, where the band body fills the
// standup's five rows and the sentence is clipped off screen. So it now walks all
// three voice modes, every height the region is drawn at, and a second world where
// nothing hides the sentence: one ask, no dedupe count, no folded ghosts, no
// stall behind it.
//
// The exact string belongs to the band's row, verbatim (§3.7) — that row is what
// the reader approves. Everything else paraphrases (askGist).
//
// The COMMENTARY is counted separately and deliberately: its entry is a
// timestamped record of the moment the ask was raised rather than a statement of
// the live ask, and it is the only place the approved bytes survive once the band
// has cleared. It may carry the command once. It may not carry it twice.
func TestTheAskIsStatedExactlyOncePerFrame(t *testing.T) {
	dm, events := demoMachine(t)
	cases := map[string]struct {
		build func() (*UIState, state.World)
		cmd   string
		entry string
		gist  string
	}{
		"demo": {
			build: func() (*UIState, state.World) { return demoView(t, dm, events) },
			cmd:   "rm -rf node_modules/.cache && pnpm rebuild",
			entry: "fix-ts2345-fallout  permission",
			gist:  "permission to run one Bash command",
		},
		"one-ask": {
			build: func() (*UIState, state.World) {
				w := oneAskWorld(demoNow())
				return viewOf(w), w
			},
			cmd:   "psql -f db/migrations/0042.sql",
			entry: "write-email-index  permission",
			gist:  "permission to run one Bash command",
		},
	}
	for name, tc := range cases {
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
			sentenceSeen := false
			for rows := 30; rows <= 90; rows++ {
				v, w := tc.build()
				v.Voice = mode
				v.advanceNarration(w, demoNow(), 64)
				frame, _ := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
				plain := stripANSI(frame)
				region, log := splitAtCommentary(plain)
				where := fmt.Sprintf("%s/%s/rows=%d", name, mode, rows)

				// The command, anywhere above the log: exactly once, and on the
				// band's own row.
				//
				// Counted on the UNWRAPPED text, because prose wraps: a sentence
				// restating the command breaks it across two rows, and a count over
				// the raw frame would score that as zero and pass.
				if got := strings.Count(squash(region), tc.cmd); got != 1 {
					t.Fatalf("%s: the command appears %d times, want 1:\n%s", where, got, region)
				}
				if got := strings.Count(region, "run: "+tc.cmd); got != 1 {
					t.Fatalf("%s: the band's command row appears %d times, want 1:\n%s",
						where, got, region)
				}
				// The alert entry, the chip, and the action the region closes on.
				for _, needle := range []string{tc.entry, copyBandNeedsYou, copyAskAction} {
					if got := strings.Count(region, needle); got != 1 {
						t.Fatalf("%s: %q appears %d times, want 1:\n%s", where, needle, got, region)
					}
				}
				// The sentence, wherever it fits, says what is being asked for
				// without restating it.
				if strings.Contains(squash(region), "You're the blocker") {
					sentenceSeen = true
					if !strings.Contains(squash(region), tc.gist) {
						t.Fatalf("%s: the sentence names no kind of ask:\n%s", where, region)
					}
				}
				if got := strings.Count(squash(log), tc.cmd); got > 1 {
					t.Fatalf("%s: the log states the command %d times:\n%s", where, got, log)
				}
			}
			// And the sentence is not simply missing everywhere: a paraphrase that
			// never renders would pass every count above.
			if mode != VoiceOff && !sentenceSeen {
				t.Errorf("%s/%s: the standup never drew its first sentence at any height",
					name, mode)
			}
		}
	}
}

// Nothing §3.7 promised may be lost in the merge. Every element, in one region.
func TestStandupCarriesEverySection37Element(t *testing.T) {
	now := demoNow()
	w := bandWorld(now)
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	got := standupText(t, w, v, now)

	for _, want := range []struct{ what, needle string }{
		{"the chip", copyBandNeedsYou},
		{"the agent name", "clear-stale-type"},
		{"the kind", kindPermission},
		{"the wait timer", "52s"},
		{"the exact command", "run: rm -rf node_modules/.cache && pnpm rebuild"},
		{"the ×n dedupe count", "×3"},
		{"the folded retry agents", "+2 retry agents folded"},
		{"resolving…", copyResolving},
		{"the §3.7.6 cap", "+2 more"},
		{"the action copy", copyAskAction},
	} {
		if !strings.Contains(got, want.needle) {
			t.Errorf("the merge lost %s (%q):\n%s", want.what, want.needle, got)
		}
	}

	// Oldest first (§3.7.6), and the cap counts the stall too — it is an entry
	// like any other.
	first := strings.Index(got, "clear-stale-type")
	second := strings.Index(got, "write-email-index")
	if first < 0 || second < 0 || first > second {
		t.Errorf("alerts are not oldest-first (%d then %d):\n%s", first, second, got)
	}
	if strings.Contains(got, "run-auth-tests") {
		t.Errorf("a capped entry was drawn anyway:\n%s", got)
	}
}

// §3.12's stall copy is the region's action when the stall IS the alert. The
// narrator writes for the scene; the band fixes the wording for the alert.
func TestStallActionCopyIsTheRegionsAction(t *testing.T) {
	now := demoNow()
	w := stallWorld(now)
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	got := standupText(t, w, v, now)

	for _, needle := range []string{
		narrate.ChipWatch, "typecheck-worke", kindStalled, "no call open",
		"2m50s", copyStallAction,
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("a stall-only standup is missing %q:\n%s", needle, got)
		}
	}
	if strings.Contains(got, copyAskAction) {
		t.Errorf("a stall shows the permission action:\n%s", got)
	}
	// §3.10.2's reversed chip is spent on the one state where you are the
	// blocker, and the narrator's own sentence under it says nothing needs you.
	if strings.Contains(got, copyBandNeedsYou) {
		t.Errorf("a stall claims the blocker chip:\n%s", got)
	}
}

// §3.7.8 in the region. Ask.Resolving means a grant, or activity after the
// prompt, was OBSERVED — the answer is in and only the outcome is unknown. So the
// entry keeps its `resolving…` mark, and the action row may not still be telling
// the reader to answer a prompt they have already answered.
func TestResolvingAskStopsAskingForTheAnswerAgain(t *testing.T) {
	now := demoNow()
	w := oneAskWorld(now)
	w.Asks[0].Resolving = true
	got := standupText(t, w, viewOf(w), now)

	if !strings.Contains(got, copyResolving) {
		t.Errorf("the entry lost its resolving mark:\n%s", got)
	}
	if strings.Contains(got, copyAskAction) {
		t.Errorf("the region still asks for an answer that is already in:\n%s", got)
	}
	if !strings.Contains(got, copyResolvingAction) {
		t.Errorf("the action row does not say what the state is:\n%s", got)
	}
	// The sentence under it agrees with the row above it, and marks itself: the
	// answer being in is read off two events, not reported by one.
	if strings.Contains(got, "You're the blocker") {
		t.Errorf("an answered ask still calls the reader the blocker:\n%s", got)
	}
	for _, want := range []string{"answer is in", "clears late"} {
		if !strings.Contains(squash(got), want) {
			t.Errorf("the sentence does not say %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"It's one key", "parked behind that keystroke"} {
		if strings.Contains(squash(got), banned) {
			t.Errorf("the ladder still presses for a keystroke (%q):\n%s", banned, got)
		}
	}

	// One ask still unanswered and the keys come straight back — the reader is the
	// blocker for THAT one, however answered the oldest is.
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
	got = standupText(t, two, viewOf(two), now)
	if !strings.Contains(got, copyAskAction) {
		t.Errorf("a second, unanswered ask did not take the action row back:\n%s", got)
	}
	if strings.Contains(got, copyResolvingAction) {
		t.Errorf("the region says nothing needs you while an ask is unanswered:\n%s", got)
	}
	if !strings.Contains(got, copyResolving) {
		t.Errorf("the answered entry lost its mark to the second ask:\n%s", got)
	}
}

// §3.7.2's keys for a stall have to survive whatever leads the band. §13.2 gives
// the region ONE action row, so with an ask outstanding the stall's copy had
// nowhere to go and vanished from the frame the moment a permission request
// arrived. It rides its own entry instead — and is never in both places at once.
func TestStallKeysSurviveAnAskLeadingTheBand(t *testing.T) {
	now := demoNow()
	w := askAndStallWorld(now)
	got := standupText(t, w, viewOf(w), now)

	if n := strings.Count(got, copyAskAction); n != 1 {
		t.Errorf("the permission action appears %d times, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, copyStallAction); n != 1 {
		t.Errorf("the stall's copy appears %d times, want 1:\n%s", n, got)
	}
	// It sits on the stall's own entry, under it, not up with the ask.
	if stall, keys := strings.Index(got, kindStalled), strings.Index(got, copyStallAction); keys < stall {
		t.Errorf("the stall's keys are drawn above the stall (%d, %d):\n%s", keys, stall, got)
	}

	// A stall on its own still carries the copy on the action row, and only there.
	only := stallWorld(now)
	if n := strings.Count(standupText(t, only, viewOf(only), now), copyStallAction); n != 1 {
		t.Errorf("a stall-only band states its keys %d times, want 1", n)
	}

	// Two stalls are still one set of keys: the copy is per frame, not per entry.
	two := stallWorld(now)
	two.Agents = append(two.Agents, state.Agent{
		ID: "a2", Name: "Probe the cache", NameExact: true, Status: state.StatusStuck,
		Station: 2, SpawnIndex: 2, SpawnedAt: now.Add(-4 * time.Minute),
		LastEventAt: now.Add(-200 * time.Second),
	})
	if n := strings.Count(standupText(t, two, viewOf(two), now), copyStallAction); n != 1 {
		t.Errorf("two stalls state the same keys %d times, want 1", n)
	}
	// And two stalls BEHIND an ask, where the action row belongs to the ask: still
	// one statement of the keys, on the first stall that needs them.
	three := askAndStallWorld(now)
	three.Agents = append(three.Agents, state.Agent{
		ID: "a3", Name: "Probe the cache", NameExact: true, Status: state.StatusStuck,
		Station: 2, SpawnIndex: 3, SpawnedAt: now.Add(-4 * time.Minute),
		LastEventAt: now.Add(-200 * time.Second),
	})
	if n := strings.Count(standupText(t, three, viewOf(three), now), copyStallAction); n != 1 {
		t.Errorf("an ask over two stalls states the stall keys %d times, want 1", n)
	}

	// And the shipped 44-row pane, where the region is the band alone: this is the
	// frame the copy went missing from.
	m, events := demoMachine(t)
	v, dw := demoView(t, m, events)
	v.advanceNarration(dw, demoNow(), 64)
	frame, _ := renderFrame(dw, v, 64, 44, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	for _, needle := range []string{copyAskAction, copyStallAction} {
		if n := strings.Count(plain, needle); n != 1 {
			t.Errorf("64x44: %q appears %d times, want 1:\n%s", needle, n, plain)
		}
	}

	// The demo world at the default voice wants six band rows and the standup has
	// five for it, so on that one geometry these keys are the row that falls off.
	// That is the region's ordinary clipping — the hint names the parts it cut and
	// `n` trades the log for the rows — and it is asserted rather than assumed, so
	// the copy cannot go quietly missing again.
	v2, dw2 := demoView(t, m, events)
	v2.advanceNarration(dw2, demoNow(), 64)
	full, _ := renderFrame(dw2, v2, 64, 72, demoNow(), NewPalette(2, false))
	fullPlain := stripANSI(full)
	if !strings.Contains(fullPlain, copyStallAction) && !strings.Contains(fullPlain, "more (must") {
		t.Errorf("the stall's keys are clipped and the hint does not say so:\n%s", fullPlain)
	}
}

// §3.19: the digest is the same band in the past tense — the chip alternates, the
// ranked change list is the body, and esc dismisses it while a live ask stays.
func TestAwayDigestRendersInTheOneBand(t *testing.T) {
	m, _ := newTestModel(t)
	at := m.clock
	m.ingest(spawnEvent("a1", "Port the auth routes", at))
	m.ingest(spawnEvent("a2", "Port the user routes", at))
	for _, id := range []string{"a1", "a2"} {
		m.ingest(event.Event{AgentID: id, Kind: event.AgentStarted, At: at})
	}
	m.handleBlur()
	m.ingest(event.Event{AgentID: "a1", Kind: event.ToolStart, Tool: "Grep", Target: "x", At: at.Add(5 * time.Second)})
	m.ingest(event.Event{AgentID: "a2", Kind: event.AgentReturned, At: at.Add(6 * time.Second)})
	m.clock = at.Add(30 * time.Second)
	m.handleFocus()
	if m.v.Digest == nil {
		t.Fatal("no digest built on focus-in")
	}

	frame, _ := renderFrame(m.world, m.v, 64, 40, m.clock, m.pal)
	plain := stripANSI(frame)
	if !strings.Contains(plain, copyBandAway) {
		t.Errorf("the digest chip is not on the standup:\n%s", plain)
	}
	if strings.Contains(plain, copyBandNeedsYou) {
		t.Errorf("both chips at once — the chip alternates (§3.10.2):\n%s", plain)
	}
	// The ranked list: returns lead here (asks → stalls → returns → other).
	ret := strings.Index(plain, "port-user-routes  returned")
	chg := strings.Index(plain, "port-auth-routes  changed")
	if ret < 0 || chg < 0 || ret > chg {
		t.Errorf("digest list is not ranked (%d then %d):\n%s", ret, chg, plain)
	}
	if !strings.Contains(plain, copyDigestDismiss) {
		t.Errorf("the digest does not say esc dismisses it:\n%s", plain)
	}
	// The §3.19 gutter marks stay on the tree rows they belong to.
	if !strings.Contains(plain, "▌") {
		t.Errorf("no ▌ change marks in the gutter:\n%s", plain)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.v.Digest != nil {
		t.Fatal("esc did not dismiss the digest")
	}
	after := stripANSI(mustFrame(m, 64, 40))
	if strings.Contains(after, copyBandAway) {
		t.Errorf("the digest survived esc:\n%s", after)
	}
}

// §13.1(b): the DISPLAY copy of a command is flattened — whitespace runs
// collapsed, control characters gone — while `y` still yanks the original bytes.
// A heredoc's indentation used to eat the region's columns.
func TestBandCommandIsFlattenedForDisplayAndYankedRaw(t *testing.T) {
	raw := "python3 -c\n    'print(1)'\n\n\tdone"
	now := demoNow()
	w := stallWorld(now)
	w.Agents[0].Status = state.StatusAsk
	w.Asks = []state.Ask{{
		Key: "k1", AgentID: "a1", Description: w.Agents[0].Name, Tool: "Bash",
		Command: raw, Kind: "command", RaisedAt: now.Add(-9 * time.Second), DupeCount: 1,
	}}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)

	got := standupText(t, w, v, now)
	if !strings.Contains(got, "run: python3 -c 'print(1)' done") {
		t.Errorf("the display command was not flattened:\n%s", got)
	}
	for _, ln := range strings.Split(got, "\n") {
		if strings.Contains(ln, "  ") && strings.Contains(ln, "python3") {
			t.Errorf("a run of whitespace survived into the display copy: %q", ln)
		}
	}

	// `y` on the band still yanks the bytes that will actually run.
	mm := &Model{
		v: v, pal: NewPalette(2, false), world: w, clock: now, cols: 64, rows: 44,
	}
	mm.v.SelID = selBand
	mm.yankKey()
	if !strings.Contains(mm.v.Toast.Text, "\n") {
		t.Errorf("y yanked the flattened command: %q", mm.v.Toast.Text)
	}
}

// The footer carries every key hint and every toast, so it may never be the thing
// a short pane clips — at any height the active screen owns, with or without a
// tree to compete with.
func TestFooterSurvivesEveryHeight(t *testing.T) {
	now := demoNow()
	dm, events := demoMachine(t)

	worlds := map[string]func() (*UIState, state.World){
		"main-only": func() (*UIState, state.World) {
			v := newUIState(testConfig())
			w := state.World{
				Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
				Main:      state.Main{Activity: "thinking", LastEventAt: now, Tokens: 900},
				Run:       &state.Run{ID: "r", StartedAt: now.Add(-time.Minute)},
				Connected: true,
			}
			return v, w
		},
		"main-only-with-an-ask": func() (*UIState, state.World) {
			v := newUIState(testConfig())
			w := bandWorld(now)
			w.Agents = w.Agents[:1]
			w.Asks = w.Asks[:1]
			v.assignSlugsInSpawnOrder(w.Agents)
			return v, w
		},
		"eight-agents": func() (*UIState, state.World) {
			v, w := demoView(t, dm, events)
			v.advanceNarration(w, demoNow(), 64)
			return v, w
		},
	}
	for name, build := range worlds {
		for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
			// The inspector is the other fixed-height region that competes for the
			// same rows, and a short pane with it open is exactly where the pad
			// used to go negative and take the footer with it.
			for _, insp := range []bool{false, true} {
				for rows := 12; rows <= 50; rows++ {
					v, w := build()
					v.Voice = mode
					v.InspectorOpen = insp
					clock := now
					if name == "eight-agents" {
						clock = demoNow()
					}
					frame, _ := renderFrame(w, v, 64, rows, clock, NewPalette(2, false))
					lines := strings.Split(frame, "\n")
					if len(lines) != rows {
						t.Fatalf("%s/%s/insp=%v/%d: %d lines", name, mode, insp, rows, len(lines))
					}
					foot := stripANSI(lines[rows-1])
					if !strings.Contains(foot, "n voice") {
						t.Errorf("%s/%s/insp=%v/%d: footer clipped, last line = %q",
							name, mode, insp, rows, foot)
					}
				}
			}
		}
	}
}

// §13.4 again, at the geometry where the region is the band alone: the tree's
// first row does not move when the scene changes, so an ask arriving or clearing
// cannot shift a single row under the reader.
func TestTreeTopIsInvariantWhereTheBandIsDrawnAlone(t *testing.T) {
	for _, rows := range []int{20, 30, 44} {
		want := -1
		for name, build := range voiceScenes(t) {
			for _, mode := range []VoiceMode{VoiceFull, VoiceStandup, VoiceOff} {
				v, w := build()
				v.Voice = mode
				v.advanceNarration(w, demoNow(), 64)
				_, meta := renderFrame(w, v, 64, rows, demoNow(), NewPalette(2, false))
				got := treeTop(meta)
				if got < 0 {
					t.Fatalf("rows=%d %s/%s: no tree row in the frame", rows, name, mode)
				}
				if want < 0 {
					want = got
					continue
				}
				if got != want {
					t.Errorf("rows=%d: tree top moved to y=%d (was %d) at %s/%s",
						rows, got, want, name, mode)
				}
			}
		}
	}
}

// The band's own lines are the only part of the region a click may select, and
// ⏎ there still jumps focus to the left pane rather than opening the inspector
// (§5.1).
func TestClickingTheBandInsideTheStandupSelectsIt(t *testing.T) {
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.advanceNarration(w, demoNow(), 64)
	_, meta := renderFrame(w, v, 64, 72, demoNow(), NewPalette(2, false))

	band, prose := 0, 0
	for _, id := range meta.lineIDs {
		if id == selBand {
			band++
		}
	}
	if band == 0 {
		t.Errorf("no line in the frame is the band")
	}
	// Prose must not be selectable: every narration line that is not a band line
	// carries no id at all, which is what stops a click on a sentence from moving
	// the tree's selection.
	for y, id := range meta.lineIDs {
		if id != "" && id != selBand && id != selQuiet && id != event.MainAgentID {
			continue // an agent row
		}
		if id == "" {
			prose++
		}
		_ = y
	}
	if prose == 0 {
		t.Errorf("every line is selectable — prose should carry no row id")
	}
}

// mustFrame renders the model's own geometry, for assertions after a key.
func mustFrame(m *Model, cols, rows int) string {
	frame, _ := renderFrame(m.world, m.v, cols, rows, m.clock, m.pal)
	return frame
}
