package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// finishedWorld: one running agent, two that have returned.
func finishedWorld(now time.Time) state.World {
	w := syntheticWorld(1, now)
	for _, id := range []string{"done-1", "done-2"} {
		w.Agents = append(w.Agents, state.Agent{
			ID: id, Name: "Port the " + id + " routes", Type: "general-purpose",
			Status: state.StatusDone, Station: 3, SpawnIndex: len(w.Agents) + 1,
			SpawnedAt: now.Add(-3 * time.Minute), DoneAt: now.Add(-20 * time.Second),
			LastEventAt: now.Add(-20 * time.Second), Tokens: 4000,
		})
	}
	return w
}

// The tree draws work in flight. A returned agent leaves the tree proper and
// joins the list at its foot, where it is still named and still selectable.
func TestReturnedAgentsListedAtTheFoot(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)

	frame, meta := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")
	plain := stripANSI(frame)
	if !strings.Contains(plain, "RETURNED 2") {
		t.Errorf("no returned list:\n%s", plain)
	}
	for _, a := range w.Agents {
		if a.Status != state.StatusDone {
			continue
		}
		if !strings.Contains(plain, v.slugFor(a)) {
			t.Errorf("returned agent %q is not named anywhere:\n%s", v.slugFor(a), plain)
		}
	}
	// The list sits BELOW every live agent's block.
	lastLive, firstReturned := -1, len(lines)
	for i, id := range meta.lineIDs {
		switch id {
		case "agent-00":
			lastLive = i
		case "done-1", "done-2", selFinished:
			if i < firstReturned {
				firstReturned = i
			}
		}
	}
	if lastLive < 0 || firstReturned < lastLive {
		t.Errorf("the returned list is not at the foot (live=%d returned=%d):\n%s",
			lastLive, firstReturned, plain)
	}

	// With the filter lifted they are full rows again and the list is gone.
	v.ShowFinished = true
	plain = stripANSI(func() string { f, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false)); return f }())
	if strings.Contains(plain, "RETURNED") {
		t.Errorf("the list survived the filter being lifted:\n%s", plain)
	}
	for _, a := range w.Agents {
		if !strings.Contains(plain, v.slugFor(a)) {
			t.Errorf("agent %q missing with the filter off:\n%s", v.slugFor(a), plain)
		}
	}
}

// A world with nothing settled says nothing: the line is not permanent chrome.
func TestReturnedLineAbsentWithNothingSettled(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(1, now)
	v := newUIState(testConfig())
	frame, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	if got := stripANSI(frame); strings.Contains(got, "RETURNED") {
		t.Errorf("returned list drawn with no returned agents:\n%s", got)
	}
}

// `a` toggles from anywhere; ⏎ on the line does the same; both say which way
// they went.
func TestToggleReturnedByKeyAndByRow(t *testing.T) {
	m, _ := newTestModel(t)
	m.world = finishedWorld(demoNow())

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !m.v.ShowFinished {
		t.Fatal("`a` did not lift the filter")
	}
	if m.v.Toast.Text != "returned agents on the tree" {
		t.Errorf("toast = %q", m.v.Toast.Text)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.v.ShowFinished {
		t.Fatal("`a` did not put the filter back")
	}

	m.v.SelID = selFinished
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.v.ShowFinished {
		t.Fatal("⏎ on the returned-agents line did nothing")
	}
}

// Selection follows the paint: whatever the frame drew is selectable, and
// nothing else is. The returned agents are rows in the bottom list on a full
// tree, one count line on the condensed screen, and neither under 40 columns.
func TestReturnedRowsAreSelectableWherePainted(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	v := newUIState(testConfig())

	for _, geom := range []struct {
		name       string
		cols, rows int
		wantList   bool // the agents themselves are selectable
		wantHeader bool // at least the count/header row is
	}{
		{"wide tree", 64, 40, true, true},
		{"condensed", 64, 10, false, true},
		{"tiny", 38, 10, false, false},
	} {
		t.Run(geom.name, func(t *testing.T) {
			_, meta := renderFrame(w, v, geom.cols, geom.rows, now, NewPalette(2, false))
			ids := selectableIDs(meta)
			header, list := false, false
			for _, id := range ids {
				switch id {
				case selFinished:
					header = true
				case "done-1", "done-2":
					list = true
				}
			}
			if header != geom.wantHeader {
				t.Errorf("returned header selectable = %v, want %v (%v)", header, geom.wantHeader, ids)
			}
			if list != geom.wantList {
				t.Errorf("returned rows selectable = %v, want %v (%v)", list, geom.wantList, ids)
			}
		})
	}
}

// Under 40 columns there is no room for the line, so the count rides on the
// header. Hidden rows are never simply gone.
func TestTinyHeaderCountsHiddenReturns(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	v := newUIState(testConfig())
	frame, _ := renderFrame(w, v, 38, 12, now, NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "✓2") {
		t.Errorf("tiny header does not count the hidden returns:\n%s", got)
	}
}

// A row that returns while it is selected does not disappear: it moves into the
// list, keeps the cursor, and the inspector stays open on it.
func TestSelectionFollowsAnAgentIntoTheList(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	v := newUIState(testConfig())
	v.SelID = "done-1"
	v.InspectorOpen = true
	v.assignSlugsInSpawnOrder(w.Agents)

	frame, meta := renderFrame(w, v, 64, 34, now, NewPalette(2, false))
	painted := false
	for _, id := range meta.lineIDs {
		if id == "done-1" {
			painted = true
		}
	}
	if !painted {
		t.Errorf("the selected agent left the frame when it returned:\n%s", stripANSI(frame))
	}
	if !strings.Contains(stripANSI(frame), "▣ ") {
		t.Errorf("the inspector closed itself on a returned agent:\n%s", stripANSI(frame))
	}
}

// One click selects a row AND opens it: the pane's whole promise is clicking
// into what an agent is doing, and a click that only paints a selection bar
// reads as a click that did nothing.
func TestSingleClickOpensTheInspector(t *testing.T) {
	now := demoNow()
	m, _ := newTestModel(t)
	m.world = syntheticWorld(3, now)
	m.v.assignSlugsInSpawnOrder(m.world.Agents)
	m.cols, m.rows = 64, 30
	m.clock = now
	m.dirty = true
	m.refresh()

	target, wantID := -1, ""
	for y, id := range m.meta.lineIDs {
		// Skip chrome and the pseudo-rows: ⏎ on those is not "open an agent".
		if id == "" || strings.HasPrefix(id, "!") || id == event.MainAgentID || id == m.v.SelID {
			continue
		}
		target, wantID = y, id
		break
	}
	if target < 0 {
		t.Fatal("no clickable agent row in the frame")
	}
	m.handleMouse(tea.MouseMsg{X: 3, Y: target, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.v.SelID != wantID {
		t.Fatalf("click selected %q, want %q", m.v.SelID, wantID)
	}
	if !m.v.InspectorOpen {
		t.Fatal("one click did not open the inspector")
	}

	// A click back inside the open inspector keeps the reader's scroll.
	m.dirty = true
	m.refresh()
	m.v.InspectorScroll = 4
	for y, id := range m.meta.lineIDs {
		if id == m.v.SelID && y > target {
			m.handleMouse(tea.MouseMsg{X: 3, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			break
		}
	}
	if m.v.InspectorScroll != 4 {
		t.Errorf("a click inside the open inspector reset the scroll to %d", m.v.InspectorScroll)
	}
}

// The split grows with the pane instead of sitting at the spec's 14 rows, so a
// tall pane spends its slack on the log — what the agent is actually doing.
func TestInspectorGrowsWithThePane(t *testing.T) {
	if got := inspectorHeight(24); got != inspectorMin {
		t.Errorf("short pane: inspector = %d, want the %d floor", got, inspectorMin)
	}
	if got := inspectorHeight(50); got != 25 {
		t.Errorf("tall pane: inspector = %d, want half of 50", got)
	}
	// A short pane never GROWS the split — it stays at the spec's floor, which
	// is what the frame's own clip ladder is written against.
	if got := inspectorHeight(16); got != inspectorMin {
		t.Errorf("short pane: inspector = %d, want the %d floor", got, inspectorMin)
	}

	now := demoNow()
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.InspectorOpen = true
	short := len(renderInspector(w, v, 64, 24, now, NewPalette(2, false)))
	tall := len(renderInspector(w, v, 64, 50, now, NewPalette(2, false)))
	if tall <= short {
		t.Errorf("inspector did not grow with the pane: %d rows at 24, %d at 50", short, tall)
	}
}

// The idle screen summarises the run this pane watched end, and offers no
// history browsing: [ and ] are unbound now.
func TestIdleShowsOnlyTheLastRun(t *testing.T) {
	v := newUIState(testConfig())
	w := state.World{
		Session:   state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionIdle},
		Connected: true,
		LastRun: &state.Run{
			ID: "r-last", Prompt: "port the auth routes to the new client",
			StartedAt: demoNow().Add(-2 * time.Minute), EndedAt: demoNow(),
			Tokens: 12000,
			Agents: []state.RunAgent{{ID: "a1", Name: "port auth", Duration: 40 * time.Second, Status: state.StatusDone}},
		},
	}
	frame, _ := renderFrame(w, v, 64, 24, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	// Named by what it was for, with the record id kept for `agentpane runs`.
	if !strings.Contains(plain, "LAST RUN") || !strings.Contains(plain, "port the auth routes") {
		t.Errorf("the last run is not named by its prompt:\n%s", plain)
	}
	if !strings.Contains(plain, "r-last") {
		t.Errorf("the run id is gone — `agentpane runs show` needs it:\n%s", plain)
	}
	for _, gone := range []string{"[ ]", "browse runs"} {
		if strings.Contains(plain, gone) {
			t.Errorf("run browsing survived (%q):\n%s", gone, plain)
		}
	}

	m, _ := newTestModel(t)
	m.world = w
	before := m.v.Toast
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if m.v.Toast != before {
		t.Errorf("[ / ] still do something: %q", m.v.Toast.Text)
	}
}

// Every agent block ENDS on a line of what it is DOING — not on a name, and
// not on the strip of what it has already done. The strip's height varies with
// the room the pane has; the last line does not.
func TestEveryAgentShowsWhatItIsDoing(t *testing.T) {
	now := demoNow()
	m, events := demoMachine(t)
	v, w := demoView(t, m, events)
	v.SelID = event.MainAgentID

	frame, meta := renderFrame(w, v, 64, 44, now, NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")

	live := 0
	for _, a := range v.orderedAgents(w) {
		if !expandable(a) {
			continue
		}
		live++
		var block []string
		for i, id := range meta.lineIDs {
			if id == a.ID {
				block = append(block, lines[i])
			}
		}
		if len(block) < 3 {
			t.Errorf("agent %q got %d lines, want at least branch + row + activity:\n%s",
				v.slugFor(a), len(block), strings.Join(block, "\n"))
			continue
		}
		if !strings.Contains(block[len(block)-1], nowMark+" ") {
			t.Errorf("agent %q does not end on its activity line:\n%s",
				v.slugFor(a), strings.Join(block, "\n"))
		}
	}
	if live == 0 {
		t.Fatal("no expandable agents in the demo world")
	}
}

// The strip never repeats the activity line: the newest completed call and the
// activity line are the same fact when no call is open.
func TestHistoryNeverRepeatsTheActivityLine(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "a1", Name: "write email index", Type: "general-purpose",
		Status: state.StatusRun, Station: 2, SpawnIndex: 1,
		SpawnedAt: now.Add(-time.Minute), LastEventAt: now.Add(-time.Second),
		ActivityLine: "Edit db/migrations/0042.sql",
		CallHistory: []state.Call{
			{Tool: "Read", Target: "db/indexes.ts"},
			{Tool: "Edit", Target: "db/migrations/0042.sql"},
		},
		TotalCalls: 2,
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Agents:    []state.Agent{a},
		Connected: true,
	}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	frame, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	// The path is shown by its base name, and the newest call is not repeated.
	if n := strings.Count(stripANSI(frame), "Edit 0042.sql"); n != 1 {
		t.Errorf("the newest call is printed %d times, want once:\n%s", n, stripANSI(frame))
	}
}

// An agent that never reported an activity gets no activity line at all. A bare
// nowMark with the station pips floating at the right margin is a row claiming to
// say what an agent is doing and saying nothing — and the platform announces
// plenty of agents like that.
func TestNoBareActivityLine(t *testing.T) {
	now := demoNow()
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Connected: true,
		Agents: []state.Agent{
			{
				ID: "ghost", Type: "general-purpose", Status: state.StatusDone,
				Station: 3, SpawnIndex: 1, Tokens: 900,
				SpawnedAt: now.Add(-time.Minute), DoneAt: now.Add(-27 * time.Second),
				LastEventAt: now.Add(-27 * time.Second),
			},
			{
				ID: "worker", Name: "read the manifest", Type: "general-purpose",
				Status: state.StatusRun, Station: 2, SpawnIndex: 2,
				SpawnedAt: now.Add(-time.Minute), LastEventAt: now.Add(-time.Second),
				ActivityLine: "Read go.mod",
			},
		},
	}
	v := newUIState(testConfig())
	v.ShowFinished = true // the settled ghost has to be on the tree to be tested
	v.assignSlugsInSpawnOrder(w.Agents)

	frame, meta := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")
	for i, id := range meta.lineIDs {
		if id != "ghost" {
			continue
		}
		if strings.Contains(lines[i], nowMark) {
			t.Errorf("an agent with no reported activity got an activity line: %q", lines[i])
		}
	}
	// The agent that DID report one still has it.
	if !strings.Contains(stripANSI(frame), nowMark+" Read go.mod") {
		t.Errorf("a real activity line went missing:\n%s", stripANSI(frame))
	}
}

// The list is capped by the room the live agents leave it: it says how many it
// could not draw, and it never leaves the cursor on an undrawn row.
func TestReturnedListCapsAndKeepsTheCursor(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(1, now)
	for i := 0; i < 10; i++ {
		w.Agents = append(w.Agents, state.Agent{
			ID: "done-" + itoa(i), Name: "worker " + itoa(i), Type: "general-purpose",
			Status: state.StatusDone, Station: 3, SpawnIndex: i + 2,
			SpawnedAt: now.Add(-2 * time.Minute),
			DoneAt:    now.Add(-time.Duration(i+1) * 10 * time.Second),
		})
	}
	v := newUIState(testConfig())
	v.SelID = "done-9" // the oldest return: last in the list, below any cut
	v.assignSlugsInSpawnOrder(w.Agents)

	frame, meta := renderFrame(w, v, 64, 22, now, NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, "RETURNED 10") {
		t.Fatalf("the list does not state the full count:\n%s", plain)
	}
	if !strings.Contains(plain, "more") {
		t.Errorf("the list was cut without saying so:\n%s", plain)
	}
	painted := false
	for _, id := range meta.lineIDs {
		if id == "done-9" {
			painted = true
		}
	}
	if !painted {
		t.Errorf("the selected agent was cut out of the list:\n%s", plain)
	}
}

// A history line says what the call DID when the tool wrote a description —
// the same summary the activity line uses — and falls back to the command with
// its absolute paths cut down when it did not.
func TestHistoryLinesPreferTheToolsDescription(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "a1", Name: "dep audit", Type: "general-purpose",
		Status: state.StatusRun, Station: 2, SpawnIndex: 1,
		SpawnedAt: now.Add(-time.Minute), LastEventAt: now.Add(-time.Second),
		ActivityLine: "Read Cargo.toml and shim manifests",
		CallHistory: []state.Call{
			{Tool: "Bash", Target: "cd /Users/x/Dev/acme-web && find /Users/x/Dev/acme-web -maxdepth 2",
				Says: "Find test directories"},
			{Tool: "Bash", Target: "cd /Users/x/Dev/acme-web && cat /Users/x/Dev/acme-web/pyproject.toml"},
		},
		TotalCalls: 2,
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Agents:    []state.Agent{a},
		Connected: true,
	}
	v := newUIState(testConfig())
	v.PinSeq, v.Pins = 1, map[string]int{"a1": 1} // pinned: the history is on the tree
	v.assignSlugsInSpawnOrder(w.Agents)
	plain := stripANSI(func() string {
		f, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
		return f
	}())

	if !strings.Contains(plain, "✔ Find test directories") {
		t.Errorf("the tool's own description is not on the row:\n%s", plain)
	}
	// The described call's raw command may not survive anywhere on the row.
	if strings.Contains(plain, "-maxdepth") {
		t.Errorf("the raw command was drawn next to its description:\n%s", plain)
	}
	// The undescribed one falls back to the command, minus the cd preamble and
	// minus the absolute paths.
	if !strings.Contains(plain, "Bash cat pyproject.toml") {
		t.Errorf("the fallback command was not cut down:\n%s", plain)
	}
	if strings.Contains(plain, "/Users/x") {
		t.Errorf("an absolute path survived onto the row:\n%s", plain)
	}
}

func TestShortCommandKeepsWhatDiffers(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"cd /a/b && cat /a/b/pyproject.toml", "cat pyproject.toml"},
		{"Bash cd /a/b && ls -la /a/b", "Bash ls -la b"},
		{"Bash wc -l rust/src/*.rs", "Bash wc -l rust/src/*.rs"},
		{"Grep \"createUser\" src/**", "Grep \"createUser\" src/**"},
		{"Bash pnpm test --filter auth --run", "Bash pnpm test --filter auth --run"},
	} {
		if got := shortTarget(c.in); got != c.want {
			t.Errorf("shortTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A block arrives at full height with its strip filling in underneath, so
// nothing below it moves while it lands, and the pane repaints fast enough to
// show it.
func TestNewAgentBlockFillsInWithoutMovingTheRows(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(2, now)
	fresh := state.Agent{
		ID: "fresh", Name: "read the manifests", Type: "general-purpose",
		Status: state.StatusRun, Station: 2, SpawnIndex: 9,
		SpawnedAt: now, LastEventAt: now, ActivityLine: "Read Cargo.toml",
		CallHistory: []state.Call{
			{Tool: "Bash", Says: "List the workspace", At: now.Add(-2 * time.Second)},
			{Tool: "Bash", Says: "Find the manifests", At: now.Add(-time.Second)},
		},
		TotalCalls: 2,
	}
	w.Agents = append(w.Agents, fresh)
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	// What the Model stamps when a row first reaches the world: the older
	// agents are long since drawn, this one has just landed.
	v.Anim, v.Wall = true, now
	for _, a := range w.Agents {
		if a.ID == "fresh" {
			continue // the one being drawn for the first time
		}
		v.Marks[enterKey(a)] = now.Add(-time.Minute)
		v.Marks[callKey(a)] = now.Add(-time.Minute)
	}

	height := func(at time.Time) (lines int, plain string) {
		v.Wall = at
		f, meta := renderFrame(w, v, 64, 30, at, NewPalette(2, false))
		for _, id := range meta.lineIDs {
			if id == "fresh" {
				lines++
			}
		}
		return lines, stripANSI(f)
	}

	// The block GROWS a line at a time — that is the trunk being drawn down to
	// it — and never in one jump, which is what made the rows below jolt.
	var heights []int
	for ms := 0; ms <= 400; ms += 40 {
		n, _ := height(now.Add(time.Duration(ms) * time.Millisecond))
		heights = append(heights, n)
	}
	for i := 1; i < len(heights); i++ {
		if heights[i] < heights[i-1] {
			t.Errorf("the block shrank while arriving: %v", heights)
		}
		if heights[i]-heights[i-1] > 2 {
			t.Errorf("the block jumped %d rows in one step: %v", heights[i]-heights[i-1], heights)
		}
	}
	if heights[0] >= heights[len(heights)-1] {
		t.Errorf("the block never grew: %v", heights)
	}
	_, early := height(now.Add(rowEnterLine))
	_, late := height(now.Add(rowEnterFor))
	if strings.Contains(early, "Read Cargo.toml") {
		t.Errorf("the whole block landed at once:\n%s", early)
	}
	if !strings.Contains(late, "Read Cargo.toml") {
		t.Errorf("the block never finished arriving:\n%s", late)
	}
	v.Wall = now
	if !animActive(w, v) {
		t.Error("a block mid-arrival must keep the pane repainting")
	}
	v.Wall = now.Add(2 * time.Minute)
	// Everything this pane can animate has been stamped and has landed.
	for _, a := range w.Agents {
		v.Marks[enterKey(a)] = now.Add(-time.Minute)
		v.Marks[callKey(a)] = now.Add(-time.Minute)
	}
	if animActive(w, v) {
		t.Error("an idle pane must stop repainting once the transitions land")
	}
}

// A returned agent is held on the tree for a moment and then moves into the
// list — it is never in both places, and never in neither.
func TestReturnedAgentIsHeldThenMoves(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	for i := range w.Agents {
		if w.Agents[i].ID == "done-1" {
			w.Agents[i].DoneAt = now // just landed
			w.Agents[i].LastEventAt = now
		}
	}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)

	v.Anim, v.Wall = true, now
	countRows := func(at time.Time, id string) int {
		v.Wall = at
		_, meta := renderFrame(w, v, 64, 30, at, NewPalette(2, false))
		n := 0
		for _, mid := range meta.lineIDs {
			if mid == id {
				n++
			}
		}
		return n
	}
	held := countRows(now, "done-1")
	if held < 2 {
		t.Errorf("a just-returned agent left the tree immediately: %d lines", held)
	}
	if moved := countRows(now.Add(returnHoldFor), "done-1"); moved != 1 {
		t.Errorf("after the hold the agent must be exactly one list row, got %d", moved)
	}
}

// The inspector says what an agent is running on, and calls out a subagent
// that is not on the session's model. It says nothing at all when the pane was
// never told — hooks carry neither field.
func TestInspectorReportsModelAndEffort(t *testing.T) {
	now := demoNow()
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Model: "claude-opus-5", Effort: "high", Activity: "orchestrate"},
		Connected: true,
		Agents: []state.Agent{
			{ID: "a1", Name: "explore the schema", Type: "Explore", Status: state.StatusRun,
				SpawnedAt: now.Add(-time.Minute), LastEventAt: now, ActivityLine: "Read schema.ts",
				Model: "claude-haiku-4-5", Effort: "high"},
			{ID: "a2", Name: "port the routes", Type: "general-purpose", Status: state.StatusRun,
				SpawnedAt: now.Add(-time.Minute), LastEventAt: now, ActivityLine: "Edit routes.ts"},
		},
	}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	v.InspectorOpen = true

	v.SelID = "a1"
	got := stripANSI(func() string { f, _ := renderFrame(w, v, 64, 34, now, NewPalette(2, false)); return f }())
	if !strings.Contains(got, "model claude-haiku-4-5") || !strings.Contains(got, "effort high") {
		t.Errorf("the inspector does not report what the agent runs on:\n%s", got)
	}
	if !strings.Contains(got, "session: claude-opus-5") {
		t.Errorf("a subagent off the session's model is not called out:\n%s", got)
	}
	// And the tree says it too, on the agent's own row: "which model is THIS
	// one on" is a question asked of a row.
	if !strings.Contains(got, "haiku-4-5 · high") {
		t.Errorf("the row does not name the agent's model:\n%s", got)
	}

	// An agent the pane was never told about invents nothing.
	v.SelID = "a2"
	got = stripANSI(func() string { f, _ := renderFrame(w, v, 64, 34, now, NewPalette(2, false)); return f }())
	if strings.Contains(got, "model claude") {
		t.Errorf("an unknown model was filled in from the session:\n%s", got)
	}
}

// A line draws itself across the row at a CONSTANT width: the part not yet
// drawn is spaces, so nothing already on screen can move while it arrives.
func TestArrivingLinesKeepTheirWidth(t *testing.T) {
	full := line(seg{"╰● explore-schema", zero}, seg{"   ", zero}, seg{"0.1k/s", zero})
	want := segsWidth(full)
	for _, cols := range []int{0, 1, 5, want / 2, want - 1, want, want + 40} {
		got := drawSegs(full, cols, zero)
		if segsWidth(got) != want {
			t.Errorf("cols=%d: width %d, want %d (%q)", cols, segsWidth(got), want, segsPlain(got))
		}
		// Everything past the pen is blank, so the drawn text can never be
		// longer than the pen has travelled.
		text := strings.TrimRight(strings.SplitN(segsPlain(got), penGlyph(cols), 2)[0], " ")
		if n := len([]rune(text)); n > cols {
			t.Errorf("cols=%d drew %d runes: %q", cols, n, text)
		}
	}
}

// The arrival clock is when the PANE first saw the agent, not when the platform
// says it spawned: on the transcript path those are seconds apart, and anchoring
// to the spawn meant the animation had already expired before the row was drawn.
func TestArrivalRunsFromFirstSightNotSpawn(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "late", Name: "read the manifests", Type: "general-purpose",
		Status: state.StatusRun, Station: 1, SpawnIndex: 1,
		SpawnedAt:   now.Add(-30 * time.Second), // the platform spawned it ages ago
		LastEventAt: now, ActivityLine: "Read Cargo.toml",
	}
	v := newUIState(testConfig())
	v.Wall = now
	if _, ok := entering(a, v); ok {
		t.Error("a view with animation off must draw the finished row")
	}
	v.Anim = true
	if _, ok := entering(a, v); !ok {
		t.Error("a row being drawn for the first time must arrive, whatever its spawn time")
	}
	v.Wall = now.Add(rowEnterFor)
	if _, ok := entering(a, v); ok {
		t.Error("the arrival must finish")
	}
}

// The reveal is a pen crossing the row, not a row that keeps getting longer:
// the frontier is marked, the width never changes, and the fraction drawn
// advances smoothly rather than in two or three jumps.
func TestTheLineIsDrawnByAPen(t *testing.T) {
	full := line(seg{"╰● explore-schema", zero}, seg{"      ", zero}, seg{"0.1k/s", zero})
	width := segsWidth(full)

	var seen []int
	for _, cols := range []int{4, 12, width - 3} {
		got := drawSegs(full, cols, zero)
		if segsWidth(got) != width {
			t.Fatalf("cols=%d: width %d, want %d", cols, segsWidth(got), width)
		}
		plain := segsPlain(got)
		if !strings.Contains(plain, penGlyph(cols)) {
			t.Errorf("cols=%d: no pen at the frontier: %q", cols, plain)
		}
		if n := strings.Count(plain, penGlyph(cols)); n != 1 {
			t.Errorf("cols=%d: %d pens, want exactly one: %q", cols, n, plain)
		}
		seen = append(seen, strings.Index(plain, penGlyph(cols)))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("the pen went backwards: %v", seen)
		}
	}
	// A finished line carries no pen at all.
	if plain := segsPlain(drawSegs(full, width, zero)); strings.ContainsAny(plain, "▏▎▍") {
		t.Errorf("a finished line kept its pen: %q", plain)
	}

	// The reveal advances every animation frame — a wipe that only moves three
	// times over its duration reads as three states, not as one movement.
	steps := map[int]bool{}
	for d := time.Duration(0); d < rowWipeFor; d += animFrame {
		steps[drawnCols(full, revealFrac(d))] = true
	}
	if len(steps) < 6 {
		t.Errorf("only %d distinct reveal positions across the wipe: %v", len(steps), steps)
	}
}

// Opening the pane draws the tree in — the same arrival every new agent gets —
// except for a one-shot frame, which must be the finished picture.
func TestOpeningThePaneDrawsTheTreeIn(t *testing.T) {
	build := func(static bool) *Model {
		mach := state.New(state.DefaultConfig())
		at := demoNow()
		for _, id := range []string{"a1", "a2"} {
			mach.Apply(spawnEvent(id, "do the "+id+" work", at), at)
			mach.Apply(event.Event{AgentID: id, Kind: event.ToolStart, Tool: "Read", Target: "f", At: at}, at)
		}
		return New(Config{
			Machine: mach, Cfg: config.Default(), Static: static,
			Now: func() time.Time { return at },
		})
	}

	live := build(false)
	arriving := 0
	for _, a := range live.world.Agents {
		if _, ok := entering(a, live.v); ok {
			arriving++
		}
	}
	if arriving != len(live.world.Agents) {
		t.Errorf("%d of %d rows arrive on open, want all of them", arriving, len(live.world.Agents))
	}

	static := build(true)
	for _, a := range static.world.Agents {
		if _, ok := entering(a, static.v); ok {
			t.Errorf("a one-shot frame caught %q mid-arrival", a.ID)
		}
	}
}

// An unanswered ask beats, and says how long you have kept it waiting as
// pressure rather than as a number to compare against a threshold you cannot
// see.
func TestAnAskBeatsAndBuildsPressure(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "a1", Name: "fix the fallout", Type: "general-purpose", Status: state.StatusAsk,
		Station: 2, SpawnIndex: 1, SpawnedAt: now.Add(-time.Minute), LastEventAt: now.Add(-52 * time.Second),
		ActivityLine: "blocked", Ask: &event.AskPayload{Kind: "command", Command: "rm -rf .cache"},
	}
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Agents:    []state.Agent{a},
		Connected: true,
		Asks: []state.Ask{{
			Key: "k", AgentID: "a1", Tool: "Bash", Command: "rm -rf .cache",
			RaisedAt: now.Add(-52 * time.Second), DupeCount: 1,
		}},
	}
	v := newUIState(testConfig())

	// The beat is a real alternation, not a constant.
	beats := map[bool]bool{}
	for s := 0; s < 4; s++ {
		beats[askBeat(now.Add(time.Duration(s)*time.Second))] = true
	}
	if len(beats) != 2 {
		t.Error("the ask never changes weight — that is not a heartbeat")
	}

	// The pressure grows with the waiting and tops out.
	short := askPressureBar(2*time.Second, time.Minute)
	long := askPressureBar(50*time.Second, time.Minute)
	over := askPressureBar(10*time.Minute, time.Minute)
	if strings.Count(short, gaugeFilled) >= strings.Count(long, gaugeFilled) {
		t.Errorf("pressure did not build: %q then %q", short, long)
	}
	if strings.Count(over, gaugeFilled) != askPressureCells {
		t.Errorf("pressure never tops out: %q", over)
	}
	if got := segsPlain(rowRight(w, a, v, now, NewPalette(2, false))); !strings.Contains(got, gaugeFilled) {
		t.Errorf("the ask row shows no pressure: %q", got)
	}
	// A row where the reader is NOT the blocker gets none of it.
	a.Status, w.Asks = state.StatusStuck, nil
	if got := segsPlain(rowRight(w, a, v, now, NewPalette(2, false))); strings.Contains(got, gaugeFilled) {
		t.Errorf("a stalled row claimed the reader is blocking it: %q", got)
	}
}

// The header says how far through its fan-out the run is, counted from the
// world and never from a teammate.
func TestHeaderCountsTheRunDown(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(5, now)
	w.Agents = append(w.Agents, state.Agent{ID: "mate", Teammate: true, Status: state.StatusTeammate, Name: "reviewer"})
	done := 0
	for i := range w.Agents {
		if w.Agents[i].Status == state.StatusDone {
			done++
		}
	}
	bar, text := runProgress(w)
	if text != itoa(done)+"/5" {
		t.Errorf("progress = %q, want %d of the 5 non-teammate agents", text, done)
	}
	if len([]rune(bar)) != runProgressCells {
		t.Errorf("bar %q is not %d cells", bar, runProgressCells)
	}
	v := newUIState(testConfig())
	if got := stripANSI(func() string { f, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false)); return f }()); !strings.Contains(got, text) {
		t.Errorf("the header does not carry the progress:\n%s", got)
	}
	// Nothing to count, nothing said.
	if bar, _ := runProgress(state.World{}); bar != "" {
		t.Errorf("a run with no agents reported progress %q", bar)
	}
}

// The beat is one cell, on the trunk, moving down toward an agent that is
// ARRIVING — and only for that. It used to beat for activity too, which on a
// busy fan-out is a cell running down the trunk more or less continuously.
func TestTheBeatTravelsDownToAnArrivingAgent(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(6, now)
	for i := range w.Agents {
		w.Agents[i].Status, w.Agents[i].LastEventAt = state.StatusRun, now
	}

	v := newUIState(testConfig())
	v.Anim, v.Wall = true, now
	for _, a := range w.Agents {
		v.Marks[enterKey(a)] = now.Add(-time.Minute)
	}
	// Everything is working hard and nothing is arriving: no beat.
	if _, _, ok := treePulse(w, v); ok {
		t.Error("a busy tree beats on every update — that is decoration, not signal")
	}

	// The last agent arrives.
	arriving := w.Agents[len(w.Agents)-1]
	delete(v.Marks, enterKey(arriving))

	var positions []int
	for _, ms := range []int{0, 100, 200, 300} {
		at := now.Add(time.Duration(ms) * time.Millisecond)
		v.Wall = at
		f, _ := renderFrame(w, v, 64, 44, at, NewPalette(2, false))
		lit := -1
		for i, ln := range strings.Split(f, "\n") {
			for _, trunk := range []string{"│", "├", "╰", "◍"} {
				if !strings.Contains(ln, "\x1b[1;32m"+trunk) {
					continue
				}
				if lit >= 0 && lit != i {
					t.Fatalf("t+%dms: the beat is in two places — that is a light show", ms)
				}
				lit = i
			}
		}
		if lit < 0 {
			t.Fatalf("t+%dms: no beat", ms)
		}
		positions = append(positions, lit)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Errorf("the beat went back up the trunk: %v", positions)
		}
	}
	if positions[len(positions)-1] == positions[0] {
		t.Errorf("the beat never moved: %v", positions)
	}
	// It stops.
	v.Wall = now.Add(rowEnterFor)
	f, _ := renderFrame(w, v, 64, 44, now.Add(rowEnterFor), NewPalette(2, false))
	for _, trunk := range []string{"│", "├", "╰", "◍"} {
		if strings.Contains(f, "\x1b[1;32m"+trunk) {
			t.Error("the beat outlived the arrival")
		}
	}
}

// The ink immediately behind the pen is still wet: drawn, but in the pen's own
// colour, settling to its real style as the pen moves on. Without it the reveal
// is a boundary crossing a finished line rather than a stroke.
func TestTheInkBehindThePenIsWet(t *testing.T) {
	edge := Style{fg: 5}
	full := line(seg{"╰● explore-schema and more text here", zero})
	got := drawSegs(full, 30, edge)

	wet, dry := 0, 0
	for _, s := range got {
		if strings.TrimSpace(s.t()) == "" || strings.ContainsAny(s.t(), "▏▎▍") {
			continue
		}
		if s.st == edge {
			wet += segsWidth([]seg{s})
		} else {
			dry += segsWidth([]seg{s})
		}
	}
	if wet == 0 {
		t.Errorf("nothing behind the pen is wet: %+v", got)
	}
	if dry == 0 {
		t.Errorf("nothing behind the pen has settled — the whole line is the pen's: %+v", got)
	}
	if wet > trailCells {
		t.Errorf("the trail is %d columns, want at most %d", wet, trailCells)
	}
	// A finished line is all dry.
	for _, s := range drawSegs(full, segsWidth(full), edge) {
		if s.st == edge {
			t.Errorf("a finished line kept wet ink: %q", s.t())
		}
	}
}

// A "user prompt" is not always the user typing: Claude Code's harness submits
// synthetic turns of its own, and the pane must not read its host's plumbing
// back as a label.
func TestPromptLabelSkipsHarnessPlumbing(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"refactor auth", "refactor auth"},
		{"<task-notification>\n<task-id>abc</task-id>\n</task-notification>", ""},
		{"<system-reminder>\nbe careful\n</system-reminder>\nport the routes", "port the routes"},
		{"<command-name>/loop</command-name>\ncheck the deploy", "check the deploy"},
		{"  \n\nadd a unique email index", "add a unique email index"},
		// Markup the user actually typed is text, not a tag block.
		{"<Header /> renders twice — find out why", "<Header /> renders twice — find out why"},
	} {
		if got := promptLabel(c.in); got != c.want {
			t.Errorf("promptLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// With nothing but plumbing, the row falls back rather than inventing one.
	w := state.World{
		Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:    state.Main{Title: "Tradefloor survey", Activity: "orchestrate"},
		Run:     &state.Run{ID: "r1", Prompt: "<task-notification>\n</task-notification>"},
	}
	if got := mainLabel(w); got != "Tradefloor survey" {
		t.Errorf("main row = %q, want the session title", got)
	}
	w.Main.Title = ""
	if got := mainLabel(w); got != "orchestrate" {
		t.Errorf("main row = %q, want the observed activity", got)
	}
}

// A PINNED row's strip does not change height when a call opens and closes. The newest
// completed call is a duplicate of the activity line and is dropped — but the
// duplicate only exists while nothing is open, so dropping a LINE meant every
// tool call added and removed a row and everything below it jumped, twice per
// call.
func TestStripHeightSurvivesACallOpeningAndClosing(t *testing.T) {
	now := demoNow()
	base := state.Agent{
		ID: "a1", Name: "write the index", Type: "general-purpose",
		Status: state.StatusRun, Station: 2, SpawnIndex: 1,
		SpawnedAt: now.Add(-time.Minute), LastEventAt: now,
		ActivityLine: "Edit 0042.sql",
		CallHistory: []state.Call{
			{Tool: "Read", Target: "indexes.ts", At: now.Add(-4 * time.Second)},
			{Tool: "Grep", Target: "\"createUser\" src", At: now.Add(-3 * time.Second)},
			{Tool: "Edit", Target: "users.ts", At: now.Add(-2 * time.Second)},
			{Tool: "Edit", Target: "0042.sql", At: now.Add(-time.Second)},
		},
		TotalCalls: 9,
	}
	blockHeight := func(a state.Agent) int {
		w := state.World{
			Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
			Agents:  []state.Agent{a}, Connected: true,
		}
		v := newUIState(testConfig())
		v.PinSeq, v.Pins = 1, map[string]int{a.ID: 1} // pinned: this row carries history
		v.assignSlugsInSpawnOrder(w.Agents)
		_, meta := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
		n := 0
		for _, id := range meta.lineIDs {
			if id == "a1" {
				n++
			}
		}
		return n
	}

	closed := blockHeight(base) // nothing open: the newest call IS the activity
	open := base
	open.OpenCall = &state.OpenCall{Tool: "Bash", Target: "pnpm test", Since: now.Add(-time.Second)}
	open.ActivityLine = "Bash pnpm test"
	if got := blockHeight(open); got != closed {
		t.Errorf("the strip is %d rows with a call open and %d without — every call would move the tree twice", got, closed)
	}
}

// Every agent gets the same strip. The greedy fill it replaced grew each agent
// in turn until the next would not fit, so one agent finishing a call
// redistributed the rows and three unrelated blocks changed height at once.
func TestEveryAgentGetsTheSameStrip(t *testing.T) {
	now := demoNow()
	w := syntheticWorld(4, now)
	for i := range w.Agents {
		w.Agents[i].Status = state.StatusRun
		w.Agents[i].CallHistory = []state.Call{
			{Tool: "Read", Target: "a.go", At: now.Add(-5 * time.Second)},
			{Tool: "Read", Target: "b.go", At: now.Add(-4 * time.Second)},
			{Tool: "Read", Target: "c.go", At: now.Add(-3 * time.Second)},
			{Tool: "Read", Target: "d.go", At: now.Add(-2 * time.Second)},
		}
		w.Agents[i].TotalCalls = 4
		w.Agents[i].ActivityLine = "Read e.go"
	}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	_, meta := renderFrame(w, v, 64, 40, now, NewPalette(2, false))

	heights := map[string]int{}
	for _, id := range meta.lineIDs {
		if id != "" && id != event.MainAgentID && !strings.HasPrefix(id, "!") {
			heights[id]++
		}
	}
	if len(heights) < 2 {
		t.Fatalf("expected several agent blocks, got %v", heights)
	}
	var first int
	for _, n := range heights {
		if first == 0 {
			first = n
			continue
		}
		if n != first {
			t.Errorf("blocks differ in height: %v — the tree re-lays-out whenever one agent moves", heights)
		}
	}
}

// The pane's loudest job is saying an agent is blocked on you. ⇥ is how you get
// to where that can be answered — the session pane — from any screen.
func TestTabJumpsToTheSessionPane(t *testing.T) {
	m, _ := newTestModel(t)
	jumped := 0
	m.cfg.FocusLeftPane = func() error { jumped++; return nil }

	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if jumped != 1 {
		t.Fatalf("⇥ jumped %d times, want once", jumped)
	}
	if m.v.Toast.Text != "→ session pane" {
		t.Errorf("toast = %q", m.v.Toast.Text)
	}

	// It never claims to have jumped when it could not.
	m.cfg.FocusLeftPane = func() error { return errNoJump }
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if !m.v.Toast.Refusal || m.v.Toast.Text != copyNoFocusJump {
		t.Errorf("a failed jump reported %q (refusal=%v)", m.v.Toast.Text, m.v.Toast.Refusal)
	}
	m.cfg.FocusLeftPane = nil
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.v.Toast.Text != copyNoFocusJump {
		t.Errorf("with no jump wired the toast = %q", m.v.Toast.Text)
	}

	// And the footer names it while an ask is outstanding, at both widths.
	now := demoNow()
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Connected: true,
		Agents: []state.Agent{{
			ID: "a1", Name: "fix the fallout", Status: state.StatusAsk, SpawnIndex: 1,
			SpawnedAt: now.Add(-time.Minute), LastEventAt: now.Add(-52 * time.Second),
		}},
		Asks: []state.Ask{{Key: "k", AgentID: "a1", Tool: "Bash", Command: "rm -rf .cache",
			RaisedAt: now.Add(-52 * time.Second), DupeCount: 1}},
	}
	v := newUIState(testConfig())
	for _, cols := range []int{44, 64} {
		frame, _ := renderFrame(w, v, cols, 24, now, NewPalette(2, false))
		if got := stripANSI(frame); !strings.Contains(got, "⇥") {
			t.Errorf("cols=%d: the footer does not name the way to answer:\n%s", cols, got)
		}
	}
}

var errNoJump = fmt.Errorf("no window")

// A subagent is announced before anyone knows what it is FOR: the row exists,
// correctly, as `general-purpose-a1b2c3`, and that is where the arrival used to
// happen — on a row a reader has no reason to be looking at. The description
// lands seconds later, the row becomes `slow-walk`, and that is the moment a
// person calls "a new subagent started". So the row arrives again when it is
// named.
func TestARowArrivesAgainWhenItIsNamed(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "a1", Type: "general-purpose", Status: state.StatusRun,
		Station: 1, SpawnIndex: 1, SpawnedAt: now, LastEventAt: now,
		ActivityLine: "Read schema.ts",
	}
	v := newUIState(testConfig())
	v.Anim, v.Wall = true, now

	// Announced, unnamed: it arrives.
	if _, ok := entering(a, v); !ok {
		t.Fatal("an announced agent must arrive")
	}
	v.Wall = now.Add(rowEnterFor)
	if _, ok := entering(a, v); ok {
		t.Fatal("that arrival must finish")
	}

	// Its description lands: it arrives again, under the name that means
	// something to the reader.
	a.Name = "explore the schema"
	if _, ok := entering(a, v); !ok {
		t.Error("a row that has just been named must be drawn again")
	}
	// …and only once more.
	v.Wall = v.Wall.Add(rowEnterFor)
	if _, ok := entering(a, v); ok {
		t.Error("the named arrival must finish too")
	}
}

// A returned agent that did nothing is not work, and the list is what the run
// finished.
func TestEmptyReturnsAreNotListed(t *testing.T) {
	now := demoNow()
	w := state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionIdle},
		Connected: true,
		Agents: []state.Agent{
			{ID: "real", Name: "count the files", Type: "Explore", Status: state.StatusDone,
				SpawnedAt: now.Add(-90 * time.Second), DoneAt: now.Add(-time.Minute),
				LastEventAt: now.Add(-time.Minute), TotalCalls: 4, Tokens: 2000},
			{ID: "empty", Type: "general-purpose", Status: state.StatusDone,
				SpawnedAt: now.Add(-time.Minute), DoneAt: now.Add(-time.Minute),
				LastEventAt: now.Add(-time.Minute)},
		},
	}
	// A tree, not the idle screen.
	w.Session.State = state.SessionActive
	w.Agents = append(w.Agents, state.Agent{
		ID: "live", Name: "still going", Status: state.StatusRun, SpawnIndex: 3,
		SpawnedAt: now.Add(-time.Minute), LastEventAt: now, ActivityLine: "Read x.go",
	})
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)

	got := stripANSI(func() string { f, _ := renderFrame(w, v, 64, 30, now, NewPalette(2, false)); return f }())
	if !strings.Contains(got, "RETURNED 1") {
		t.Errorf("the list counts an agent that did nothing as work:\n%s", got)
	}
	if !strings.Contains(got, v.slugFor(w.Agents[0])) {
		t.Errorf("the agent that did the work is missing:\n%s", got)
	}
	if strings.Contains(got, placeholderName("empty", "general-purpose")) {
		t.Errorf("the empty return is listed by name:\n%s", got)
	}
}

// The arrival, through the REAL loop: tea messages in, View() out. This is the
// test that would have caught the bug three attempts missed — every mechanism
// was right and the stroke was drawn in spaces, because a newly arrived agent is
// by definition the LAST one and the last block's continuation prefix is blank.
// The pane animated a vertical made of nothing and then the row appeared.
func TestTheArrivalIsVisibleFrameByFrame(t *testing.T) {
	base := demoNow()
	wall := base
	mach := state.New(state.DefaultConfig())
	var m tea.Model = New(Config{
		Machine: mach, Cfg: testConfig(),
		Events: make(chan event.Event, 4),
		Now:    func() time.Time { return wall },
	})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 52, Height: 14})
	// Spawn ONLY. The platform announces a subagent before it does anything, so
	// the row is queued when it first appears — which is precisely the case the
	// earlier version of this test masked by sending a tool call in the same
	// batch, and precisely the case that never animated.
	m, _ = m.Update(evMsg{spawnEvent("a1", "explore the schema", base), true})

	frames := map[string]bool{}
	sawTrunkOnly, sawWritten := false, false
	for step := 0; step < 14; step++ {
		wall = base.Add(time.Duration(step) * animFrame)
		m, _ = m.Update(animMsg{})
		plain := stripANSI(m.View())
		frames[plain] = true

		mm := m.(*Model)
		for i, id := range mm.meta.lineIDs {
			if id != "a1" {
				continue
			}
			ln := strings.TrimRight(strings.Split(plain, "\n")[i], " ")
			if strings.TrimSpace(ln) == "" {
				t.Fatalf("step %d: the block drew a blank line — a stroke made of spaces is what "+
					"\"it just appears\" looks like:\n%s", step, plain)
			}
			if strings.Contains(ln, "explore-schema") {
				sawWritten = true
			} else if strings.ContainsAny(ln, "│├╰") {
				sawTrunkOnly = true
			}
		}
	}
	if !sawTrunkOnly {
		t.Error("no bare-trunk line: the vertical was never drawn down to the agent")
	}
	if !sawWritten {
		t.Error("the row was never written")
	}
	if len(frames) < 6 {
		t.Errorf("only %d distinct frames across the arrival — that is not an animation", len(frames))
	}
}

// Toggling the returned agents onto the tree must not be a one-way door: the
// way back sits where the way in was. Clicking the heading used to leave the
// reader in a state whose only exit was a key they had to already know.
func TestTheReturnedToggleIsReversibleWhereItWasClicked(t *testing.T) {
	now := demoNow()
	w := finishedWorld(now)
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)

	frame, meta := renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	if !strings.Contains(stripANSI(frame), "RETURNED 2") {
		t.Fatalf("no list to click:\n%s", stripANSI(frame))
	}
	lines := strings.Split(stripANSI(frame), "\n")
	heading := -1
	for i, ln := range lines {
		if strings.Contains(ln, "RETURNED") {
			heading = i
		}
	}
	if heading < 0 || meta.lineIDs[heading] != selFinished {
		t.Fatalf("the heading is not a selectable row (line %d)", heading)
	}
	// The rule under it belongs to no row, so it is not a click target.
	if got := lines[heading+1]; strings.Trim(got, "─ ") != "" {
		t.Errorf("no rule under the heading: %q", got)
	}

	// Click it: the rows move onto the tree…
	v.ShowFinished = true
	frame, meta = renderFrame(w, v, 64, 30, now, NewPalette(2, false))
	plain := stripANSI(frame)
	if strings.Contains(plain, "RETURNED") {
		t.Errorf("the list survived the toggle:\n%s", plain)
	}
	// …and the way back is still there, still selectable, still in the same place.
	back := false
	for _, id := range meta.lineIDs {
		if id == selFinished {
			back = true
		}
	}
	if !back {
		t.Errorf("no way back — the toggle is a one-way door:\n%s", plain)
	}
	if !strings.Contains(plain, "⏎ lists them") {
		t.Errorf("the way back does not say how:\n%s", plain)
	}
}

// The row carries a FAILED check and nothing else; the inspector carries both,
// labelled as the inferences they are.
func TestOnlyAFailedCheckReachesTheRow(t *testing.T) {
	now := demoNow()
	a := state.Agent{
		ID: "a1", Name: "run the suite", Type: "general-purpose", Status: state.StatusRun,
		Station: 2, SpawnIndex: 1, SpawnedAt: now.Add(-time.Minute), LastEventAt: now,
		ActivityLine: "Bash pnpm test", Verify: state.VerifyOK,
	}
	w := state.World{
		Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Agents:  []state.Agent{a}, Connected: true,
	}
	v := newUIState(testConfig())
	v.assignSlugsInSpawnOrder(w.Agents)
	v.SelID, v.InspectorOpen = "a1", true

	frame, meta := renderFrame(w, v, 64, 34, now, NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")
	for i, id := range meta.lineIDs {
		if id == "a1" && strings.Contains(lines[i], nowMark) && strings.Contains(lines[i], "verified") {
			t.Errorf("a passing check took row width: %q", lines[i])
		}
	}
	if !strings.Contains(stripANSI(frame), "✓ verified (inferred)") {
		t.Errorf("the inspector lost the passing check:\n%s", stripANSI(frame))
	}

	// A failure is news and stays on the row.
	w.Agents[0].Verify = state.VerifyFailed
	if got := stripANSI(func() string { f, _ := renderFrame(w, v, 64, 34, now, NewPalette(2, false)); return f }()); !strings.Contains(got, "✖ verify failed") {
		t.Errorf("a failed check is missing from the row:\n%s", got)
	}
}
