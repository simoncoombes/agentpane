package ui

// pin_test.go — the §3.8a session picker is CONDITIONAL. It exists only when
// the attachment is both changeable (no --session pin) and ambiguous (more
// than one live session). A pinned pane is opened beside one Claude tab and
// must never advertise, or perform, a switch to another session.

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/state"
)

func pinTestSessions(n int) []SessionRow {
	all := []SessionRow{
		{ID: "3f9c1a2b", ShortID: "3f9c", Dir: "~/Dev", Live: true, Attached: true, Agents: 8},
		{ID: "2a71ffff", ShortID: "2a71", Dir: "~/Dev", Live: true, Idle: true},
		{ID: "1c08eeee", ShortID: "1c08", Dir: "~/Dev", Live: true},
	}
	return all[:n]
}

func pinIdleWorld() state.World {
	return state.World{
		Session:   state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionIdle},
		Connected: true,
	}
}

// TestSessionPickerMatrix: {pinned, unpinned} × {1 session, 3 sessions}. The
// SESSIONS block renders in exactly one of the four combinations, and the
// header's session-id suffix (§3.8a) survives pinning — it names what this
// pane watches, which is information, not an offer.
//
// A PINNED pane carries the id even when it is the only live session. The
// count of other sessions is not what makes the id worth printing: the pin is.
// A pane pinned to one tab of five, four of which have since exited, is still
// a pane whose whole purpose is being that tab's, and a bare "AGENTS" leaves
// the reader to assume it is the session they are looking at.
func TestSessionPickerMatrix(t *testing.T) {
	cases := []struct {
		name       string
		pinned     string
		sessions   int
		wantPicker bool
		wantHdrID  bool
	}{
		{"unpinned, one session", "", 1, false, false},
		{"unpinned, three sessions", "", 3, true, true},
		{"pinned, one session", "3f9c1a2b", 1, false, true},
		{"pinned, three sessions", "3f9c1a2b", 3, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newUIState(testConfig())
			v.Sessions = pinTestSessions(c.sessions)
			v.PinnedSession = c.pinned
			v.Attached = c.pinned
			if v.sessionPicker() != c.wantPicker {
				t.Fatalf("sessionPicker() = %v, want %v", v.sessionPicker(), c.wantPicker)
			}

			frame, _ := renderFrame(pinIdleWorld(), v, 64, 28, demoNow(), NewPalette(2, false))
			plain := stripANSI(frame)
			if got := strings.Contains(plain, "SESSIONS"); got != c.wantPicker {
				t.Errorf("SESSIONS block present = %v, want %v:\n%s", got, c.wantPicker, plain)
			}
			// No orphaned rows either: the ids of the other sessions must be
			// gone with the header, not merely unlabelled.
			for _, s := range v.Sessions {
				if s.Attached {
					continue
				}
				if got := strings.Contains(plain, s.ShortID); got != c.wantPicker {
					t.Errorf("row %s present = %v, want %v:\n%s", s.ShortID, got, c.wantPicker, plain)
				}
			}
			if got := strings.Contains(plain, "switch"); got != c.wantPicker {
				t.Errorf("switch hint present = %v, want %v", got, c.wantPicker)
			}
			if got := strings.Contains(plain, "AGENTS · 3f9c"); got != c.wantHdrID {
				t.Errorf("header session id present = %v, want %v:\n%s", got, c.wantHdrID, plain)
			}
		})
	}
}

// TestPinnedIdleHasNoStrayChrome: omitting the block must not leave a blank
// header, a dangling separator, or a double blank line behind.
func TestPinnedIdleHasNoStrayChrome(t *testing.T) {
	v := newUIState(testConfig())
	v.Sessions = pinTestSessions(3)
	v.PinnedSession, v.Attached = "3f9c1a2b", "3f9c1a2b"
	v.Runs = []RunRow{{
		ID: "3f9c-r1", Start: demoNow().Add(-4 * time.Minute), End: demoNow(),
		Tokens: 611000,
		Agents: []RunAgentRow{{Name: "explore-schema", Duration: 88 * time.Second, Status: "done"}},
	}}

	frame, _ := renderFrame(pinIdleWorld(), v, 64, 28, demoNow(), NewPalette(2, false))
	lines := strings.Split(stripANSI(frame), "\n")
	// The body is everything between the header rule and the footer rule,
	// minus the padding the idle frame adds to push the footer down.
	body := lines[2:lastRuleIndex(lines)]
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	for i := 1; i < len(body); i++ {
		if strings.TrimSpace(body[i]) == "" && strings.TrimSpace(body[i-1]) == "" {
			t.Fatalf("double blank line in the idle body — the omitted SESSIONS block left a hole:\n%s", frame)
		}
	}
	if len(body) == 0 {
		t.Fatalf("idle body is empty:\n%s", frame)
	}
	if strings.Contains(stripANSI(frame), "attach: newest live") {
		t.Error("the picker's right-hand caption survived without its block")
	}
}

// lastRuleIndex finds the idle frame's footer rule (the last all-─ line).
func lastRuleIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" && strings.Trim(s, "─") == "" {
			return i
		}
	}
	return len(lines)
}

// TestSnapshotIdlePinned is the golden proving a pinned pane with three live
// sessions shows no SESSIONS block at all.
func TestSnapshotIdlePinned(t *testing.T) {
	v := newUIState(testConfig())
	v.Sessions = pinTestSessions(3)
	v.PinnedSession, v.Attached = "3f9c1a2b", "3f9c1a2b"
	v.Runs = []RunRow{{
		ID: "3f9c-r1", Start: demoNow().Add(-4 * time.Minute), End: demoNow(),
		Tokens: 611000,
		Agents: []RunAgentRow{
			{Name: "explore-schema", Duration: 88 * time.Second, Status: "done"},
			{Name: "test-runner", Duration: 184 * time.Second, Status: "done"},
		},
	}}
	frame, _ := renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
	checkGolden(t, "idle_pinned", frame)
	if strings.Contains(stripANSI(frame), "SESSIONS") {
		t.Error("pinned idle frame still lists sessions")
	}
}

// TestPinnedWaitingLine: the pane can open before its session reaches the
// registry (autopane splits during SessionStart). Until it lands the idle
// screen names what it is waiting for instead of attaching to a neighbour.
func TestPinnedWaitingLine(t *testing.T) {
	v := newUIState(testConfig())
	v.Sessions = pinTestSessions(3)
	v.PinnedSession, v.Attached = "7bd2aaaa", "" // nothing attached yet
	if !v.pinnedWaiting() {
		t.Fatal("pinned with nothing attached must read as waiting")
	}
	frame, _ := renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
	plain := stripANSI(frame)
	if !strings.Contains(plain, "waiting for session 7bd2") {
		t.Errorf("waiting line missing or unnamed:\n%s", plain)
	}
	if strings.Contains(plain, copyWatching) {
		t.Errorf("generic watching line shown while waiting on a pinned session:\n%s", plain)
	}

	// Once attached, the honest line goes back to the normal watching copy.
	v.Attached = v.PinnedSession
	frame, _ = renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, copyWatching) {
		t.Errorf("watching line missing after attach:\n%s", got)
	}

	// A session that ENDS stays ended: no re-attachment, no waiting line.
	w := pinIdleWorld()
	w.Session.State = state.SessionEnded
	v.Attached = v.PinnedSession
	frame, _ = renderFrame(w, v, 64, 24, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "session ended · [ ] browse runs") {
		t.Errorf("pinned session end lost its frozen summary footer:\n%s", got)
	}
}

// TestPinnedSwitchKeysAreNoOps: 1–9 must not switch, toast, or reset state
// when the picker is absent — pinned, or with a single candidate.
func TestPinnedSwitchKeysAreNoOps(t *testing.T) {
	for _, c := range []struct {
		name     string
		pinned   string
		sessions int
	}{
		{"pinned with three sessions", "3f9c1a2b", 3},
		{"unpinned with one session", "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			switched := 0
			m.cfg.SwitchSession = func(SessionRow) { switched++ }
			m.v.Sessions = pinTestSessions(c.sessions)
			m.v.PinnedSession, m.v.Attached = c.pinned, c.pinned
			m.world = pinIdleWorld()

			for _, k := range []string{"1", "2", "3", "9"} {
				m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
			}
			if switched != 0 {
				t.Errorf("%d switches happened with no picker on screen", switched)
			}
			if m.v.Toast.Text != "" {
				t.Errorf("a no-op key toasted %q", m.v.Toast.Text)
			}
		})
	}

	// Control: unpinned with three sessions still switches (the recovery
	// path for a hand-run agentpane in a busy directory).
	m, _ := newTestModel(t)
	switched := []string{}
	m.cfg.SwitchSession = func(row SessionRow) { switched = append(switched, row.ShortID) }
	m.v.Sessions = pinTestSessions(3)
	m.world = pinIdleWorld()
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if len(switched) != 1 || switched[0] != "2a71" {
		t.Fatalf("unpinned picker stopped working: %v", switched)
	}
}

// TestSessionSwitchReloadsRunHistory: run history is session-scoped on disk,
// so a switch must reload it. Leaving it alone printed session A's runs under
// session B's header — the same leak as a pinned pane showing a sibling's
// history, only reachable by keyboard.
func TestSessionSwitchReloadsRunHistory(t *testing.T) {
	m, _ := newTestModel(t)
	// The CLI re-keys its run store inside SwitchSession, before the Model
	// reloads; this stub stands in for that.
	scope := "3f9c1a2b"
	m.cfg.SwitchSession = func(row SessionRow) { scope = row.ID }
	m.cfg.RunHistory = func() []RunRow {
		return []RunRow{{ID: "run-for-" + scope, Start: demoNow(), End: demoNow()}}
	}
	m.v.Runs = m.cfg.RunHistory()
	m.v.HistIdx = 0
	m.v.Sessions = pinTestSessions(3)
	m.world = pinIdleWorld()

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})

	if len(m.v.Runs) != 1 {
		t.Fatalf("run history = %v after the switch", m.v.Runs)
	}
	if got := m.v.Runs[0].ID; got != "run-for-"+m.v.Sessions[1].ID {
		t.Errorf("idle screen still shows %q after switching to %s — the previous "+
			"session's history survived the switch", got, m.v.Sessions[1].ID)
	}
	if m.v.HistIdx != 0 {
		t.Errorf("HistIdx = %d, want 0: the browse position belonged to the old history", m.v.HistIdx)
	}
}

// TestOverlayListsSwitchKeyOnlyWhenUseful: the `?` overlay documents 1–9
// only where it does something, and names the pin when there is one.
func TestOverlayListsSwitchKeyOnlyWhenUseful(t *testing.T) {
	cases := []struct {
		name     string
		pinned   string
		sessions int
		wantKey  bool
	}{
		{"unpinned, three sessions", "", 3, true},
		{"pinned, three sessions", "3f9c1a2b", 3, false},
		{"unpinned, one session", "", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newUIState(testConfig())
			v.Sessions = pinTestSessions(c.sessions)
			v.PinnedSession, v.Attached = c.pinned, c.pinned
			v.Overlay = true
			frame, _ := renderFrame(pinIdleWorld(), v, 64, 30, demoNow(), NewPalette(2, false))
			plain := stripANSI(frame)
			if got := strings.Contains(plain, "1–9 switch session"); got != c.wantKey {
				t.Errorf("overlay lists 1–9 = %v, want %v:\n%s", got, c.wantKey, plain)
			}
			if !strings.Contains(plain, "browse runs") {
				t.Errorf("overlay lost the [ ] line:\n%s", plain)
			}
			if got := strings.Contains(plain, "pinned to this pane"); got != (c.pinned != "") {
				t.Errorf("overlay pin note = %v, want %v", got, c.pinned != "")
			}
		})
	}
}

// TestIdleHistoryAttributesForeignRuns: an UNPINNED pane with no live session
// reads the run store's whole-store view, which merges every session's
// subdirectory. Rendering those as bare "RUN <id>" made three sessions' runs
// read as this pane's history — the same leak as a pinned pane showing a
// sibling's runs, only quieter. `agentpane runs` already names the session for
// this exact merge; the idle screen now does too.
func TestIdleHistoryAttributesForeignRuns(t *testing.T) {
	runs := []RunRow{
		{ID: "r-b", Session: "2a71ffff", Start: demoNow().Add(-2 * time.Minute), End: demoNow(),
			Agents: []RunAgentRow{{Name: "explore-schema", Duration: 30 * time.Second, Status: "done"}}},
		{ID: "r-a", Session: "3f9c1a2b", Start: demoNow().Add(-9 * time.Minute), End: demoNow().Add(-8 * time.Minute),
			Agents: []RunAgentRow{{Name: "test-runner", Duration: 40 * time.Second, Status: "done"}}},
		{ID: "r-legacy", Start: demoNow().Add(-20 * time.Minute), End: demoNow().Add(-19 * time.Minute),
			Agents: []RunAgentRow{{Name: "old-run", Duration: 10 * time.Second, Status: "done"}}},
	}

	// Unattached: every attributable row names its session.
	v := newUIState(testConfig())
	v.Runs = runs
	for i, want := range []string{"session 2a71", "session 3f9c"} {
		v.HistIdx = i
		frame, _ := renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
		if got := stripANSI(frame); !strings.Contains(got, want) {
			t.Errorf("unscoped idle history row %d is anonymous (want %q):\n%s", i, want, got)
		}
	}
	// A record written before runs were session-scoped has no provenance, and
	// none is invented (C8).
	v.HistIdx = 2
	frame, _ := renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "RUN r-legacy") || strings.Contains(got, "session ") {
		t.Errorf("legacy run got an invented session:\n%s", got)
	}

	// A pane that HAS a session is reading only that session's directory, so
	// the tag would be noise on every row.
	v.PinnedSession, v.Attached = "3f9c1a2b", "3f9c1a2b"
	v.Runs = []RunRow{runs[1]}
	v.HistIdx = 0
	frame, _ = renderFrame(pinIdleWorld(), v, 64, 24, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); strings.Contains(got, "session 3f9c") {
		t.Errorf("a scoped pane tagged its own runs:\n%s", got)
	}
}

// TestPinnedHeaderNamesItsSessionAlone: when the pinned session is the ONLY
// live one, "which session is this pane watching" is still exactly the
// question the pin answers, on the active screen as well as the idle one.
func TestPinnedHeaderNamesItsSessionAlone(t *testing.T) {
	w := syntheticWorld(2, demoNow())
	w.Session = state.Session{ID: "3f9c1a2b", ShortID: "3f9c", State: state.SessionActive}

	v := newUIState(testConfig())
	v.Sessions = pinTestSessions(1)
	v.PinnedSession, v.Attached = "3f9c1a2b", "3f9c1a2b"
	frame, _ := renderFrame(w, v, 64, 40, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "AGENTS 2 · 3f9c") {
		t.Errorf("pinned active header does not name its session:\n%s", got)
	}

	// Unpinned with one session keeps the bare header: nothing to disambiguate
	// and nothing was promised.
	v2 := newUIState(testConfig())
	v2.Sessions = pinTestSessions(1)
	frame, _ = renderFrame(w, v2, 64, 40, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); strings.Contains(got, "AGENTS 2 · 3f9c") {
		t.Errorf("unpinned single-session header grew an id:\n%s", got)
	}

	// Pinned but still waiting: no world session id yet, so the pin itself is
	// the honest answer.
	v3 := newUIState(testConfig())
	v3.PinnedSession, v3.Attached = "7bd2aaaa", ""
	w3 := w
	w3.Session = state.Session{}
	frame, _ = renderFrame(w3, v3, 64, 40, demoNow(), NewPalette(2, false))
	if got := stripANSI(frame); !strings.Contains(got, "AGENTS 2 · 7bd2") {
		t.Errorf("waiting pinned header names nothing:\n%s", got)
	}
}
