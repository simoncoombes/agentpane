package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
)

// handleKey implements the complete §5.1 keymap. It never sends a signal to
// any process (C5): the only quit path is ctrl-c, and even that only exits
// agentpane itself.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if m.v.Overlay {
		m.v.Overlay = false
		m.dirty = true
		return nil
	}

	if key != "ctrl+c" {
		m.v.QuitArmed = false
	}

	switch key {
	case "ctrl+c":
		if m.liveAgents() && !m.v.QuitArmed {
			m.v.QuitArmed = true
			m.setToast("agents keep running · ctrl-c again to quit", true)
			return nil
		}
		m.quit = true

	// j/k are bound to the TREE at all times (§13.2). Narration has no
	// selection and no cursor: it is read, not navigated, and the page keys
	// below are the only thing that moves it. This is why the narration lines
	// carry no row id in the frame — there is nothing there for j/k to land on.
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "g":
		ids := m.selectionOrder()
		if len(ids) > 0 {
			m.select_(ids[0])
		}
	case "G":
		ids := m.selectionOrder()
		if len(ids) > 0 {
			m.select_(ids[len(ids)-1])
		}

	case " ":
		m.togglePin()

	case "enter":
		m.enterKey()

	case "esc", "q":
		switch {
		case m.v.InspectorOpen:
			// View-only: the agent keeps running (PART 4).
			m.v.InspectorOpen = false
			m.v.Follow = false
		case m.v.Digest != nil:
			m.v.Digest = nil // the live ask entry stays (§3.19)
		case m.v.ShowFinished:
			// The way out of anything: esc puts the tree back the way it was.
			// A reader who has ended up somewhere they did not mean to be
			// reaches for this key, not for the one that got them there.
			m.toggleFinished()
		}
		m.dirty = true

	case "f":
		// The suppressed-agents view has nothing to follow: it is a static
		// census, not a live agent, and renderQuietInspector ignores Follow. A
		// key that silently does nothing is worse than one that says so.
		if m.v.SelID == selQuiet {
			m.setToast("nothing to follow — suppressed agents have no events", true)
			break
		}
		if m.v.SelID == selFinished {
			m.setToast("nothing to follow — that row is a filter", true)
			break
		}
		m.v.Follow = true
		m.v.InspectorOpen = true
		m.syncFollow()
		m.dirty = true

	case "pgup", "pgdown":
		m.pageKey(key == "pgup")

	case "o":
		m.openFileKey()

	case "y":
		m.yankKey()
	case "Y":
		m.yankLogKey()

	case "w":
		// A cycle rather than a toggle: fill is a third layout, not a mode
		// switch, and the key that owns width should reach all three.
		m.v.WidthMode = nextWidthMode(m.v.WidthMode)
		m.setToast(widthToast(m.v.WidthMode, m.v.MaxWidth), false)
		if m.cfg.PersistWidth != nil {
			m.cfg.PersistWidth(m.v.WidthMode)
		}

	case "t":
		if m.v.RightCol == "since" {
			m.v.RightCol = "rate"
			m.setToast("right column: token rate", false)
		} else {
			m.v.RightCol = "since"
			m.setToast("right column: since last event", false)
		}
		if m.cfg.PersistRightCol != nil {
			m.cfg.PersistRightCol(m.v.RightCol)
		}

	case "a":
		m.toggleFinished()

	case "s":
		// The way into the suppressed-agents census now that it is not a row on
		// the tree. `s` for suppressed, which is what the code and the docs call
		// them; "quiet" is only what the footer has room to say.
		m.openQuiet()

	case "tab":
		m.focusSession()

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Silent no-ops unless the picker is on screen (sessionPicker): a
		// pinned pane belongs to one tab's session, and with a single
		// candidate there is nothing to switch to.
		if idlePhase(m.world) && m.v.sessionPicker() {
			idx := int(key[0] - '1')
			if idx < len(m.v.Sessions) {
				row := m.v.Sessions[idx]
				if row.Attached {
					break // re-attaching the current session is a no-op
				}
				if m.cfg.SwitchSession != nil {
					m.cfg.SwitchSession(row)
					// The CLI swapped the source stack; drop every piece
					// of the previous session's state so it cannot leak
					// into the new one (asks, agents, logs, dedupe keys).
					m.resetForSession(row.ID)
				}
				m.setToast("attached "+row.ShortID, false)
			}
		}

	case "x":
		// §5.1: kill is removed — explain the absence instead.
		m.setToast(copyReadOnly, true)

	case "?":
		m.v.Overlay = true
		m.dirty = true

	case "p":
		if m.cfg.Demo != nil {
			if m.v.DemoPaused {
				m.cfg.Demo.Resume()
				m.v.DemoPaused = false
			} else {
				m.cfg.Demo.Pause()
				m.v.DemoPaused = true
			}
			m.dirty = true
		}
	case ".":
		// Demo step. It used to be `n`, which §13.2 gives to the voice — and the
		// demo is the one place the voice matters most, since it is what the
		// mock's own `n` does. `.` is otherwise unbound at every geometry.
		if m.cfg.Demo != nil {
			m.cfg.Demo.Step()
			m.v.DemoPaused = true // Step pauses a running demo (§1.4)
			m.dirty = true
		}
	}
	return nil
}

// pageKey is ⇞/⇟. The inspector is the only pageable region left: the
// commentary and the standup body that used to share these keys are gone.
//
// j/k are never involved: they stay on the tree at all times.
func (m *Model) pageKey(up bool) {
	if !m.v.InspectorOpen {
		return
	}
	page := inspectorHeight(m.rows) - 5
	if page < 1 {
		page = 1
	}
	if up {
		m.v.InspectorScroll += page
	} else {
		m.v.InspectorScroll -= page
		if m.v.InspectorScroll < 0 {
			m.v.InspectorScroll = 0
		}
	}
	m.dirty = true
}

func (m *Model) liveAgents() bool {
	for _, a := range m.world.Agents {
		switch a.Status {
		case state.StatusRun, state.StatusAsk, state.StatusStuck, state.StatusQueued:
			return true
		}
	}
	return false
}

// selectionOrder is the j/k order over the current render: the rows the last
// frame actually painted (selectableIDs). Before the first paint there is only
// main to sit on.
//
// The one exception is the returned list, which is windowed: its whole order is
// spliced in where the drawn rows are, so j past the bottom of the window lands
// on the agent below it and the next frame's returnedWindow scrolls to bring it
// on screen. Without this the cursor stops at the window's edge and the rows
// under it are unreachable — a capped list you cannot scroll is a truncated one.
func (m *Model) selectionOrder() []string {
	ids := selectableIDs(m.meta)
	if len(ids) == 0 {
		return []string{event.MainAgentID}
	}
	return spliceReturned(ids, m.v.returnedAgents(m.world))
}

// spliceReturned replaces the returned-agent ids the frame drew with the full
// returned list, in list order, leaving every other row where it was.
func spliceReturned(ids []string, returned []state.Agent) []string {
	if len(returned) == 0 {
		return ids
	}
	inList := make(map[string]bool, len(returned))
	for _, a := range returned {
		inList[a.ID] = true
	}
	first := -1
	for i, id := range ids {
		if inList[id] {
			first = i
			break
		}
	}
	if first < 0 {
		return ids // no list on screen: the count line, or the rows are on the tree
	}
	out := append([]string(nil), ids[:first]...)
	for _, a := range returned {
		out = append(out, a.ID)
	}
	for _, id := range ids[first:] {
		if !inList[id] {
			out = append(out, id)
		}
	}
	return out
}

// focusSession jumps the terminal's focus to the session pane — the pane you
// type in, and the only place a permission request can actually be answered.
//
// The pane's loudest job is telling you an agent is blocked on you (§3.20), and
// until this key it had no way to get you THERE: the jump existed but only as ⏎
// on the alert row, which is drawn on the condensed screen alone. A monitor that
// says "go and answer this" and offers no way to go is half a monitor.
//
// It moves focus and nothing else: agentpane still answers nothing itself (C5).
func (m *Model) focusSession() {
	if m.cfg.FocusLeftPane == nil {
		m.setToast(copyNoFocusJump, true)
		return
	}
	if err := m.cfg.FocusLeftPane(); err != nil {
		m.setToast(copyNoFocusJump, true)
		return
	}
	m.setToast("→ session pane", false)
}

// toggleFinished flips the returned-agent filter (`a`, and ⏎ on the
// returned-agents line). It says which way it went, because the rows it
// affects are by definition the ones not on screen when it is on.
func (m *Model) toggleFinished() {
	m.v.ShowFinished = !m.v.ShowFinished
	if m.v.ShowFinished {
		m.setToast("returned agents on the tree", false)
	} else {
		m.setToast("returned agents listed below", false)
	}
	m.dirty = true
}

// openQuiet opens the suppressed-agents census: the full list with per-agent
// spawn and last-event times (§13.3 Q8), which renderInspector routes selQuiet
// to. It is reached by `s`, and by ⏎ on the count line on the screens that
// still draw one (idle, condensed).
//
// Scroll starts at the bottom — the newest announcements, which is where a
// suppressed agent that is still alive necessarily is — and Follow is cleared,
// because a static census has nothing to follow.
func (m *Model) openQuiet() {
	if len(m.world.Quiet) == 0 {
		return // a key that opens an empty view is worse than one that does nothing
	}
	m.v.SelID = selQuiet
	m.v.InspectorOpen = true
	m.v.InspectorScroll = 0
	m.v.Follow = false
	m.dirty = true
}

// moveSel moves selection by id, never by index (§3.13), and cancels follow
// (§5.1).
func (m *Model) moveSel(delta int) {
	m.v.Follow = false
	ids := m.selectionOrder()
	if len(ids) == 0 {
		return
	}
	cur := 0
	for i, id := range ids {
		if id == m.v.SelID {
			cur = i
			break
		}
	}
	next := cur + delta
	if next < 0 {
		next = 0 // stops at start, no wrap (§5.1)
	}
	if next >= len(ids) {
		next = len(ids) - 1
	}
	m.select_(ids[next])
}

func (m *Model) select_(id string) {
	if m.v.SelID != id {
		m.v.SelID = id
		m.v.InspectorScroll = 0
		m.dirty = true
	}
}

// togglePin pins/unpins the selected agent's tool calls; no-op on decayed and
// queued rows (§5.1) and anything that never expands.
func (m *Model) togglePin() {
	a, ok := agentByID(m.world, m.v.SelID)
	if !ok || !expandable(a) {
		return
	}
	if _, pinned := m.v.Pins[a.ID]; pinned {
		delete(m.v.Pins, a.ID)
	} else {
		m.v.PinSeq++
		m.v.Pins[a.ID] = m.v.PinSeq
	}
	m.dirty = true
}

// enterKey: on the alert band, jump focus to the left pane; on the
// suppressed-agents line, open the phantom census; otherwise open (or refocus)
// the inspector (§5.1).
func (m *Model) enterKey() {
	if m.v.SelID == selFinished {
		m.toggleFinished()
		return
	}
	if m.v.SelID == selBand {
		m.focusSession()
		return
	}
	if m.v.SelID == selFinished {
		m.toggleFinished()
		return
	}
	if m.v.SelID == selQuiet {
		m.openQuiet()
		return
	}
	m.v.InspectorOpen = true
	m.v.InspectorScroll = 0
	m.dirty = true
}

func (m *Model) openFileKey() {
	slugName, file := m.selFile()
	if file == "" {
		m.setToast(slugName+" has no open file", true)
		return
	}
	if m.cfg.OpenInEditor != nil {
		if err := m.cfg.OpenInEditor(file); err != nil {
			m.setToast(slugName+" has no open file", true)
			return
		}
	}
	m.setToast("opened "+shortPath(file)+" in $EDITOR", false)
}

func (m *Model) selFile() (slugName, file string) {
	if a, ok := agentByID(m.world, m.v.SelID); ok {
		return m.v.slugFor(a), a.OpenFile
	}
	return m.v.SelID, ""
}

// yankKey yanks the agent's error, else its failing command, else its
// activity line (§5.1), via OSC 52 + pbcopy (§5.3).
func (m *Model) yankKey() {
	text := ""
	if a, ok := agentByID(m.world, m.v.SelID); ok {
		switch {
		case a.LastError != "":
			text = a.LastError
		default:
			// The RAW target and the raw activity line: §13.1 flattens tool text
			// on ingest for DISPLAY, and is explicit that `y` must yank what the
			// tool actually ran. A multi-line `python3 -c` one-liner pasted back
			// as a single flattened line is not the command.
			for i := len(a.CallHistory) - 1; i >= 0; i-- {
				if a.CallHistory[i].Err {
					text = strings.TrimSpace(a.CallHistory[i].Tool + " " + a.CallHistory[i].TargetRaw())
					break
				}
			}
			if text == "" {
				text = a.ActivityRaw()
			}
		}
	} else if m.v.SelID == selBand && len(m.world.Asks) > 0 {
		text = m.world.Asks[0].Command
	} else {
		// On main: the FULL untruncated workstream text (session title,
		// else prompt, else activity). The row shows a budgeted label, so
		// yank is how the whole thing leaves the pane.
		text = mainLabel(m.world)
	}
	if text == "" {
		m.setToast("nothing to yank", true)
		return
	}
	if m.cfg.Emitter != nil {
		m.cfg.Emitter.CopyToClipboard(text)
	}
	m.setToast("yanked: "+truncString(text, 24)+"…", false)
}

func (m *Model) yankLogKey() {
	lines := m.v.Logs.Lines(m.v.SelID)
	if len(lines) == 0 {
		// Main with an empty log still has one thing worth copying: the
		// full workstream text its row can only show budgeted.
		if m.v.SelID == event.MainAgentID {
			if text := mainLabel(m.world); text != "" {
				if m.cfg.Emitter != nil {
					m.cfg.Emitter.CopyToClipboard(text)
				}
				m.setToast("yanked: "+truncString(text, 24)+"…", false)
				return
			}
		}
		m.setToast("nothing to yank", true)
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text)
		b.WriteString("\n")
	}
	if m.cfg.Emitter != nil {
		m.cfg.Emitter.CopyToClipboard(b.String())
	}
	m.setToast("yanked: "+truncString(lines[0].Text, 24)+"…", false)
}

// handleMouse: a click selects a row and opens it, the wheel scrolls; no drag
// (§5.1, with the single-click deviation documented below).
func (m *Model) handleMouse(msg tea.MouseMsg) {
	switch {
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		if m.v.InspectorOpen {
			m.v.InspectorScroll++
			m.dirty = true
		} else {
			m.moveSel(-1)
		}
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		if m.v.InspectorOpen {
			if m.v.InspectorScroll > 0 {
				m.v.InspectorScroll--
				m.dirty = true
			}
		} else {
			m.moveSel(1)
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		if m.meta == nil || msg.Y < 0 || msg.Y >= len(m.meta.lineIDs) {
			return
		}
		id := m.meta.lineIDs[msg.Y]
		if id == "" {
			return
		}
		m.v.Follow = false
		reselect := id != m.v.SelID
		m.select_(id)
		switch {
		case id == selBand:
			// ⏎ on the band jumps iTerm2's focus to the session pane. A stray
			// click may not move the user's focus to another pane, so the band
			// stays keyboard-only.
		case reselect || !m.v.InspectorOpen:
			// One click opens the agent. Double-click used to be the only way
			// in, which is an invisible affordance on a pane whose whole point
			// is "click in and see what it is doing" — and a click that only
			// paints a selection bar reads as a click that did nothing.
			m.enterKey()
		default:
			// A click INSIDE the open inspector, on the row it belongs to:
			// leave the scroll position where the reader put it.
		}
	}
}

// nextWidthMode is the `w` cycle: fill → wide → narrow → fill. It starts at
// the default, so the two fixed budgets sit one and two presses away and a
// third press is always the way back to the pane's own width.
func nextWidthMode(cur string) string {
	switch cur {
	case "wide":
		return "narrow"
	case "narrow":
		return "fill"
	default:
		return "wide"
	}
}

// widthToast names the layout `w` just moved to. fill has no number to quote
// because its width is whatever the pane is, so it names the ceiling instead,
// which is the only part the reader cannot see for themselves.
func widthToast(mode string, maxWidth int) string {
	switch mode {
	case "narrow":
		return "layout: narrow 44"
	case "wide":
		return "layout: wide 64"
	default:
		return fmt.Sprintf("layout: fills the pane (max %d)", maxWidth)
	}
}
