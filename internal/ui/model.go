package ui

import (
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/slug"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
)

// minFrameGap coalesces repaints to at most 10fps (C10).
const minFrameGap = 100 * time.Millisecond

// evBatch bounds how many queued events one Update drains.
const evBatch = 64

// DemoControls is what `--demo` wires in for §1.4 pause/step; nil means the
// p/n keys are inert and undocumented in the overlay.
type DemoControls interface {
	Pause()
	Resume()
	Step()
}

// Config wires the Model to the CLI stage. Machine and Events are required;
// everything else degrades to a no-op when nil (C9).
type Config struct {
	// Machine is the domain state machine the event loop feeds. The Model
	// owns it exclusively once New returns.
	Machine *state.Machine
	// Events is the merged source stream (event.Source.Events output).
	Events <-chan event.Event
	// Demo enables the p (pause/resume) and n (step) keys (§1.4).
	Demo DemoControls

	// Cfg is the loaded PART 7 config; Warnings are surfaced once in the
	// footer and listed under `?`.
	Cfg      config.Config
	Warnings []config.Warning

	// Emitter performs every §5.3 side effect (title, badge, bell, OSC 9,
	// OSC 52, OSC 8). Nil disables them all.
	Emitter *term.Emitter
	// StateWriter maintains ~/.local/state/agentpane/current.json (§3.20a).
	StateWriter *statefile.Writer
	// RunStore persists finished runs on RunEnded (§2.8).
	RunStore *runstore.Store

	// AccentIndex is the resolved live accent (term.AccentPick);
	// AccentReason is its reasoning, shown under `?`.
	AccentIndex  int
	AccentReason string
	// SourceName is shown under `?` (§5.1).
	SourceName string
	// Version is shown under `?`.
	Version string

	// Sessions lists concurrent sessions for the idle screen (§3.8a).
	Sessions func() []SessionRow
	// SwitchSession reattaches to the given row (idle 1–9, §5.1). The CLI
	// owns the actual source swap.
	SwitchSession func(row SessionRow)
	// PinnedSession is the session id this pane is FIXED to (--session).
	// Non-empty means the attachment can never change: the §3.8a session
	// picker and the 1–9 keys are withheld entirely, because offering to
	// switch a pane that belongs to one tab is an offer that must not be
	// taken. The header still names the session (§3.8a) — that is
	// information, not a choice.
	PinnedSession string
	// AttachedSession reports the session id the source stack is currently
	// attached to, "" while nothing is ("waiting for a pinned session that
	// has not reached the registry yet"). Nil means "always attached".
	AttachedSession func() string
	// RunHistory loads persisted runs for [ ] browsing (§3.8).
	RunHistory func() []RunRow
	// OpenInEditor opens a path in $EDITOR in a new iTerm2 tab (§5.3). The
	// CLI owns the AppleScript; the Model only calls and toasts.
	OpenInEditor func(path string) error
	// FocusLeftPane jumps focus to the session pane (⏎ on the band, §5.3).
	FocusLeftPane func() error
	// PersistWidth / PersistRightCol store the w and t toggles (§5.1
	// "persisted"). Nil keeps them in-memory for the session.
	PersistWidth    func(width string)
	PersistRightCol func(mode string)

	// DetailUnavailable renders the §1.5 honest-degradation line.
	DetailUnavailable string

	// Now is the wall clock used for pacing and live-source time; nil means
	// time.Now. Event and demo time always comes from event timestamps.
	Now func() time.Time
}

// Model is the bubbletea program. Construct with New; use with tea.NewProgram
// (recommended options: tea.WithAltScreen, tea.WithMouseCellMotion,
// tea.WithReportFocus).
type Model struct {
	cfg Config
	v   *UIState
	pal Palette

	world state.World
	clock time.Time // injected content clock — never time.Now() in render

	cols, rows int

	frame        string
	meta         *frameMeta
	dirty        bool
	lastFrame    time.Time
	framePending bool

	askKnown    map[string]time.Time
	askNotified map[string]bool
	hadAsks     bool
	connected   bool

	lastClickAt   time.Time
	lastClickLine int

	quit bool
}

type evMsg struct {
	ev event.Event
	ok bool
}
type tickMsg time.Time
type frameMsg struct{}

// New builds the Model. It never fails: nil collaborators degrade to no-ops
// (C9), and an unusable Machine renders the disconnected state.
func New(cfg Config) *Model {
	if cfg.Machine == nil {
		cfg.Machine = state.New(state.DefaultConfig())
	}
	noColor := !cfg.Cfg.Color
	m := &Model{
		cfg:         cfg,
		v:           newUIState(cfg.Cfg),
		pal:         NewPalette(cfg.AccentIndex, noColor),
		cols:        64,
		rows:        24,
		askKnown:    map[string]time.Time{},
		askNotified: map[string]bool{},
		connected:   true,
	}
	m.v.SourceName = cfg.SourceName
	m.v.AccentReason = cfg.AccentReason
	m.v.Version = cfg.Version
	m.v.Warnings = cfg.Warnings
	m.v.DetailUnavailable = cfg.DetailUnavailable
	m.v.DemoAvailable = cfg.Demo != nil
	m.v.Emitter = cfg.Emitter
	if cfg.RunHistory != nil {
		m.v.Runs = cfg.RunHistory()
	}
	if cfg.Sessions != nil {
		m.v.Sessions = cfg.Sessions()
	}
	m.v.PinnedSession = cfg.PinnedSession
	if cfg.AttachedSession != nil {
		m.v.Attached = cfg.AttachedSession()
	} else {
		m.v.Attached = cfg.PinnedSession
	}
	m.clock = m.now()
	m.world = cfg.Machine.Snapshot()
	if len(cfg.Warnings) > 0 {
		m.setToast(cfg.Warnings[0].String(), true)
	}
	m.dirty = true
	return m
}

func (m *Model) now() time.Time {
	if m.cfg.Now != nil {
		return m.cfg.Now()
	}
	return time.Now()
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.waitEvents(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) waitEvents() tea.Cmd {
	ch := m.cfg.Events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		return evMsg{ev, ok}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.cols, m.rows = msg.Width, msg.Height
		m.dirty = true

	case evMsg:
		if msg.ok {
			m.ingest(msg.ev)
			// Drain whatever queued behind it, bounded (C10: one Update,
			// one repaint).
		drain:
			for i := 0; i < evBatch; i++ {
				select {
				case ev, ok := <-m.cfg.Events:
					if !ok {
						break drain
					}
					m.ingest(ev)
				default:
					break drain
				}
			}
			cmds = append(cmds, m.waitEvents())
		}

	case tickMsg:
		m.tick()
		cmds = append(cmds, tickCmd())

	case frameMsg:
		m.framePending = false

	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))

	case tea.MouseMsg:
		m.handleMouse(msg)

	case tea.BlurMsg:
		m.handleBlur()

	case tea.FocusMsg:
		m.handleFocus()
	}

	if m.quit {
		return m, tea.Quit
	}
	if cmd := m.refresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// View implements tea.Model: it returns the coalesced cached frame (C10).
func (m *Model) View() string {
	return m.frame
}

// refresh re-renders at most every minFrameGap, scheduling a trailing frame
// when a repaint is suppressed so the newest state always lands.
func (m *Model) refresh() tea.Cmd {
	if !m.dirty {
		return nil
	}
	wall := m.now()
	if !m.lastFrame.IsZero() {
		if gap := wall.Sub(m.lastFrame); gap < minFrameGap {
			if m.framePending {
				return nil
			}
			m.framePending = true
			return tea.Tick(minFrameGap-gap, func(time.Time) tea.Msg { return frameMsg{} })
		}
	}
	m.frame, m.meta = renderFrame(m.world, m.v, m.cols, m.rows, m.clock, m.pal)
	m.lastFrame = wall
	m.dirty = false
	return nil
}

// --- event/tick ingestion ---

// ingest applies one source event, runs the derived events through the log
// and the §3.20/§5.3 side-effect ladder, and refreshes the snapshot.
//
// The cross-session drop is checked HERE, before anything is touched, because
// it has to bind every per-event consumer and not just the world. The machine
// dropping a sibling session's row inside Apply left the row in the inspector
// log (m.v.Logs), yankable with Y, and let its timestamp drag this pane's
// content clock forward — a pinned pane showing session B's text while the
// tree correctly showed none of it. Machine.Accepts is the one authority; the
// rest of this function may assume the event is ours.
func (m *Model) ingest(ev event.Event) {
	if !m.cfg.Machine.Accepts(ev) {
		return
	}
	at := ev.At
	if at.IsZero() {
		at = m.now()
	}
	if at.After(m.clock) {
		m.clock = at
	}
	derived := m.cfg.Machine.Apply(ev, m.clock)
	m.v.Logs.Ingest(ev)
	m.afterMachine(derived)
}

// tick advances the content clock and the machine's timers (§2.5 stalls,
// §3.5 decay, §3.7.8 grace), then runs the escalation checks.
func (m *Model) tick() {
	if m.cfg.Demo != nil {
		m.clock = m.clock.Add(time.Second)
	} else {
		wall := m.now()
		if wall.After(m.clock) {
			m.clock = wall
		} else {
			m.clock = m.clock.Add(time.Second)
		}
	}
	derived := m.cfg.Machine.Tick(m.clock)
	m.afterMachine(derived)
	m.notifyCheck()
	if m.cfg.Sessions != nil && idlePhase(m.world) {
		m.v.Sessions = m.cfg.Sessions()
	}
	if m.cfg.AttachedSession != nil {
		// The pinned-wait resolves off-thread (the CLI attaches as soon as
		// the registry row lands), so the idle screen re-reads it each tick.
		m.v.Attached = m.cfg.AttachedSession()
	}
	m.dirty = true // 1s clock repaint (§3.13)
}

func (m *Model) afterMachine(derived []event.Event) {
	for _, d := range derived {
		m.v.Logs.Ingest(d)
		if d.Kind == event.RunEnded {
			m.persistRun(d)
		}
	}
	prevRev := m.world.Rev
	m.world = m.cfg.Machine.Snapshot()
	m.syncCompaction()
	m.syncConnection()
	m.syncAsks()
	m.syncFollow()
	if m.world.Rev != prevRev {
		m.writeStatefile()
		m.dirty = true
	}
}

// syncAsks runs the §3.20 escalation ladder — asks ONLY, once per ask:
// band+bell+title+badge+attention on a new ask, everything cleared on
// resolution. Stalls and completions never escalate (§3.20).
func (m *Model) syncAsks() {
	seen := map[string]bool{}
	for _, ask := range m.world.Asks {
		seen[ask.Key] = true
		if _, known := m.askKnown[ask.Key]; known {
			continue
		}
		m.askKnown[ask.Key] = ask.RaisedAt
		slugName := m.v.slugByID(ask.AgentID, ask.Description)
		text := "⚑ " + slugName + " needs you"
		if e := m.cfg.Emitter; e != nil {
			if m.cfg.Cfg.Title {
				e.SetTitle(text)
			}
			if m.cfg.Cfg.Badge {
				e.Badge(text)
			}
			if m.cfg.Cfg.Bell {
				e.Bell()
			}
			if m.cfg.Cfg.Attention {
				e.RequestAttention()
			}
		}
	}
	removed := false
	for key := range m.askKnown {
		if !seen[key] {
			delete(m.askKnown, key)
			delete(m.askNotified, key)
			removed = true
		}
	}
	if removed && len(m.world.Asks) == 0 && m.hadAsks {
		if e := m.cfg.Emitter; e != nil {
			if m.cfg.Cfg.Title {
				e.ClearTitle()
			}
			if m.cfg.Cfg.Badge {
				e.ClearBadge()
			}
		}
	}
	m.hadAsks = len(m.world.Asks) > 0
}

// notifyCheck is escalation level 4 (§3.20): one OSC 9 notification per ask
// after notify_after unanswered, never repeated.
func (m *Model) notifyCheck() {
	if !m.cfg.Cfg.Notify || m.cfg.Emitter == nil {
		return
	}
	after := m.cfg.Cfg.NotifyAfter
	if after <= 0 {
		after = 60 * time.Second
	}
	for _, ask := range m.world.Asks {
		if m.askNotified[ask.Key] {
			continue
		}
		if m.clock.Sub(ask.RaisedAt) >= after {
			m.askNotified[ask.Key] = true
			slugName := m.v.slugByID(ask.AgentID, ask.Description)
			m.cfg.Emitter.Notify("⚑ " + slugName + " needs you")
		}
	}
}

func (m *Model) syncConnection() {
	if m.world.Connected == m.connected {
		return
	}
	m.connected = m.world.Connected
	if m.connected {
		m.setToast("✓ reconnected", false)
	} else {
		m.v.DisconnectedAt = m.clock
		m.setToast("⚠ disconnected — retrying", true)
	}
}

// syncCompaction holds row order while compacting (§3.9).
func (m *Model) syncCompaction() {
	if m.world.Session.State == state.SessionCompacting {
		if len(m.v.HeldOrder) == 0 {
			for _, a := range m.world.Agents {
				m.v.HeldOrder = append(m.v.HeldOrder, a.ID)
			}
		}
	} else {
		m.v.HeldOrder = nil
	}
}

// syncFollow tracks the agent with the most recent event (PART 4 follow).
func (m *Model) syncFollow() {
	if !m.v.Follow {
		return
	}
	best := ""
	var bestAt time.Time
	for _, a := range m.world.Agents {
		if a.Teammate {
			continue
		}
		if a.LastEventAt.After(bestAt) {
			best, bestAt = a.ID, a.LastEventAt
		}
	}
	if best != "" && best != m.v.SelID {
		m.v.SelID = best
		m.v.InspectorScroll = 0
		m.dirty = true
	}
}

// persistRun appends the finished run to the run store (§2.8, §5.3).
func (m *Model) persistRun(d event.Event) {
	var run state.Run
	if err := json.Unmarshal([]byte(d.Detail), &run); err != nil {
		return // C9: malformed derived payloads are dropped, never fatal
	}
	if m.cfg.RunStore != nil {
		rec := runstore.Run{
			ID:          run.ID,
			Start:       run.StartedAt,
			End:         run.EndedAt,
			Outcome:     runOutcome(run),
			Contentions: run.Contentions,
			Stalls:      run.Stalls,
		}
		for _, a := range run.Agents {
			name := m.v.slugByID(a.ID, a.Name)
			rec.Agents = append(rec.Agents, runstore.AgentSummary{
				Name:     name,
				Duration: a.Duration,
				Status:   a.Status.String(),
				Tokens:   a.Tokens,
			})
		}
		_ = m.cfg.RunStore.Append(rec) //nolint:errcheck // degrade silently (§5.4)
	}
	if m.cfg.RunHistory != nil {
		m.v.Runs = m.cfg.RunHistory()
		m.v.HistIdx = 0
	}
}

// runOutcome summarises a run without inventing a verdict (C8): counts only.
func runOutcome(run state.Run) string {
	unsettled := 0
	for _, a := range run.Agents {
		if a.Status != state.StatusDone && a.Status != state.StatusTeammate {
			unsettled++
		}
	}
	if unsettled == 0 {
		return "all merged"
	}
	return itoa(unsettled) + " unsettled"
}

// writeStatefile maintains the §3.20a --oneline state file.
func (m *Model) writeStatefile() {
	if m.cfg.StateWriter == nil {
		return
	}
	s := statefile.State{Session: m.world.Session.ShortID}
	for _, a := range m.world.Agents {
		switch a.Status {
		case state.StatusRun:
			s.Live++
		case state.StatusStuck:
			s.Stalls++
		case state.StatusDone:
			s.Settled++
		}
		if a.OpenCall != nil && a.Status != state.StatusDone {
			s.InCall++
		}
	}
	s.Asks = len(m.world.Asks)
	if len(m.world.Asks) > 0 {
		oldest := m.world.Asks[0]
		s.OldestAskSlug = m.v.slugByID(oldest.AgentID, oldest.Description)
		s.OldestAskAge = m.clock.Sub(oldest.RaisedAt)
	}
	m.cfg.StateWriter.Update(s)
}

// resetForSession clears every piece of per-session state — the machine's
// world, the log, slugs, pins, digest, and the escalation gates — so a
// session switch (§3.8a) starts clean instead of carrying session-A agents,
// unresolvable asks, or (AgentID, Kind, Seq) dedupe keys into session B.
func (m *Model) resetForSession(sessionID string) {
	m.cfg.Machine.Reset(sessionID)
	m.v.Logs = NewEventLog()
	m.v.Slugs = slug.New(m.cfg.Cfg.SlugMax)
	m.v.Pins = map[string]int{}
	m.v.PinSeq = 0
	m.v.ScrollTop = 0
	m.v.SelID = event.MainAgentID
	m.v.InspectorOpen = false
	m.v.InspectorScroll = 0
	m.v.Follow = false
	m.v.Digest = nil
	m.v.Away = false
	m.v.HeldOrder = nil
	m.askKnown = map[string]time.Time{}
	m.askNotified = map[string]bool{}
	m.hadAsks = false
	if m.cfg.RunHistory != nil {
		// Run history is session-scoped too: the CLI re-keyed the store before
		// SwitchSession returned, so reloading here swaps the idle screen's
		// runs instead of leaving session A's history under session B's
		// header.
		m.v.Runs = m.cfg.RunHistory()
		m.v.HistIdx = 0
	}
	m.world = m.cfg.Machine.Snapshot()
	m.dirty = true
}

// --- focus (§3.19) ---

func (m *Model) handleBlur() {
	if !m.cfg.Cfg.FocusDigest || m.v.Away {
		return
	}
	m.v.Away = true
	m.v.AwayStart = m.clock
	m.v.BlurRev = m.world.Rev
}

func (m *Model) handleFocus() {
	if !m.v.Away {
		return
	}
	m.v.Away = false
	d := buildDigest(m.world, m.v, m.clock)
	if d != nil {
		m.v.Digest = d
	}
	m.dirty = true
}

// buildDigest ranks the rows that changed while unfocused: asks → stalls →
// returns, capped at digest_cap with "+n more" (§3.19).
func buildDigest(w state.World, v *UIState, now time.Time) *digest {
	var entries []digestEntry
	marks := map[string]bool{}
	for _, a := range v.orderedAgents(w) {
		if a.Rev <= v.BlurRev {
			continue
		}
		marks[a.ID] = true
		e := digestEntry{Slug: v.slugFor(a)}
		switch a.Status {
		case state.StatusAsk:
			e.Kind, e.rank = "asked permission", 0
			e.When = formatSince(now.Sub(a.LastEventAt))
		case state.StatusStuck:
			e.Kind, e.rank = "stalled", 1
			e.When = formatSince(now.Sub(a.LastEventAt))
		case state.StatusDone:
			e.Kind, e.rank = "returned", 2
			if !a.DoneAt.IsZero() {
				e.When = formatClock(now.Sub(a.DoneAt))
			}
		default:
			e.Kind, e.rank = "changed", 3
			e.When = formatSince(now.Sub(a.LastEventAt))
		}
		entries = append(entries, e)
	}
	if w.Main.Rev > v.BlurRev {
		marks[event.MainAgentID] = true
	}
	if len(entries) == 0 && len(marks) == 0 {
		return nil
	}
	// Stable rank sort: asks → stalls → returns → other (§3.19).
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].rank < entries[j-1].rank; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	d := &digest{Dur: now.Sub(v.AwayStart), Marks: marks}
	cap := v.DigestCap
	if len(entries) > cap {
		d.More = len(entries) - cap
		entries = entries[:cap]
	}
	d.Entries = entries
	return d
}

// --- toasts (§5.2) ---

func (m *Model) setToast(text string, refusal bool) {
	// Stamped on the content clock so expiry follows the same 1s tick the
	// renderer sees (deterministic under --demo).
	m.v.Toast = toast{Text: text, At: m.clock, Refusal: refusal}
	m.dirty = true
}
