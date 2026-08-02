package ui

import (
	"strings"
	"time"

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
		}
		m.dirty = true

	case "f":
		m.v.Follow = true
		m.v.InspectorOpen = true
		m.syncFollow()
		m.dirty = true

	case "o":
		m.openFileKey()

	case "y":
		m.yankKey()
	case "Y":
		m.yankLogKey()

	case "w":
		if m.v.WidthMode == "wide" {
			m.v.WidthMode = "narrow"
			m.setToast("layout: narrow 44", false)
		} else {
			m.v.WidthMode = "wide"
			m.setToast("layout: wide 64", false)
		}
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

	case "[", "]":
		if idlePhase(m.world) && len(m.v.Runs) > 0 {
			n := len(m.v.Runs)
			if key == "[" {
				m.v.HistIdx = (m.v.HistIdx + n - 1) % n
			} else {
				m.v.HistIdx = (m.v.HistIdx + 1) % n
			}
			m.dirty = true
		}

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
	case "n":
		if m.cfg.Demo != nil {
			m.cfg.Demo.Step()
			m.v.DemoPaused = true // Step pauses a running demo (§1.4)
			m.dirty = true
		}
	}
	return nil
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

// selectionOrder is the j/k order over the current render.
func (m *Model) selectionOrder() []string {
	return m.v.selectionIDs(m.world, m.bandPresent(), quietLineDrawn(m.world, m.cols))
}

func (m *Model) bandPresent() bool {
	if m.v.Digest != nil || len(m.world.Asks) > 0 {
		return true
	}
	for _, a := range m.world.Agents {
		if a.Status == state.StatusStuck {
			return true
		}
	}
	return false
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

// enterKey: on the alert band, jump focus to the left pane; otherwise open
// (or refocus) the inspector (§5.1).
func (m *Model) enterKey() {
	if m.v.SelID == selBand {
		if m.cfg.FocusLeftPane != nil {
			m.cfg.FocusLeftPane() //nolint:errcheck // degrade silently (§5.4)
		}
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
			for i := len(a.CallHistory) - 1; i >= 0; i-- {
				if a.CallHistory[i].Err {
					text = strings.TrimSpace(a.CallHistory[i].Tool + " " + a.CallHistory[i].Target)
					break
				}
			}
			if text == "" {
				text = a.ActivityLine
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

// handleMouse: click selects, double-click opens the inspector, scroll
// scrolls; no drag (§5.1).
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
		double := msg.Y == m.lastClickLine && m.now().Sub(m.lastClickAt) < 400*time.Millisecond
		m.lastClickAt, m.lastClickLine = m.now(), msg.Y
		m.v.Follow = false
		m.select_(id)
		if double {
			m.enterKey()
		}
	}
}
