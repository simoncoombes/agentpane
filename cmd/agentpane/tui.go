package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/runstore"
	"github.com/simoncoombes/agentpane/internal/source"
	"github.com/simoncoombes/agentpane/internal/source/codexsrc"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
	"github.com/simoncoombes/agentpane/internal/source/registry"
	"github.com/simoncoombes/agentpane/internal/source/transcript"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/statefile"
	"github.com/simoncoombes/agentpane/internal/term"
	"github.com/simoncoombes/agentpane/internal/ui"
)

// runTUI is the default invocation and `--demo`: build sources, wire the ui
// Model, run the bubbletea program.
func runTUI(fl cliFlags, stderr io.Writer) int {
	cfg, warnings := config.Load(configPath())
	applyOverrides(&cfg, fl, &warnings)

	prefs := loadPrefs()
	if fl.width == "" && (prefs.Width == "wide" || prefs.Width == "narrow" || prefs.Width == "fill") {
		cfg.Width = prefs.Width // §5.1: the w toggle is persisted
	}

	// Terminal probe + accent pick (§3.10.3). The tty handle stays open for
	// the emitter's lifetime; no tty means no escapes, never an error.
	var caps term.Caps
	var emitter *term.Emitter
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		caps = term.Probe(tty)
		emitter = &term.Emitter{W: tty, Enabled: true}
		defer tty.Close()
	}
	accentIdx, accentReason := resolveAccent(cfg, caps)

	dbg := newDebugLog(fl.logPath)
	defer dbg.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	machine := state.New(stateConfig(cfg))

	uiCfg := ui.Config{
		Machine:         machine,
		Cfg:             cfg,
		Emitter:         emitter,
		AccentIndex:     accentIdx,
		AccentReason:    accentReason,
		Version:         Version,
		Trace:           dbg.printf,
		OpenInEditor:    openInEditor(cfg.Editor, dbg),
		FocusLeftPane:   focusLeftPane(dbg),
		PersistWidth:    func(w string) { prefs.Width = w; savePrefs(prefs) },
		PersistRightCol: func(m string) { prefs.RightCol = m; savePrefs(prefs) },
	}

	if fl.source == "codex" {
		// A second platform, behind the same seam. No registry, no socket and no
		// hooks: Codex writes one JSONL rollout per thread and names each
		// thread's parent in its first line, so the tree is discoverable by
		// reading files (internal/source/codexsrc).
		dir, err := codexsrc.DefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "agentpane: cannot locate the Codex sessions directory: %v\n", err)
			return 1
		}
		src := codexsrc.New(dir, fl.session)
		events, err := source.Mux(ctx, src)
		if err != nil {
			fmt.Fprintf(stderr, "agentpane: %v\n", err)
			return 1
		}
		uiCfg.Events = dbg.tee(events)
		uiCfg.SourceName = "codex"
		uiCfg.PinnedSession = fl.session
		uiCfg.Sessions = codexSessionRows(dir)
		uiCfg.AttachedSession = func() string { return fl.session }
	} else if fl.demo || fl.source == "fake" {
		// Demo replay (§1.4): the fake source through the same mux path.
		// The real state file and run store are left alone — a replay must
		// not masquerade as a live session to --oneline or pollute history.
		// TODO(agentpane): §2.10 also promises "an away-digest on first
		// paint" of the demo. The digest only materialises on a real
		// blur→focus pair (§3.19), so seeding it here would need a synthetic
		// focus protocol through the Model; left unwired.
		src := fake.New(fake.Options{Speed: fl.demoSpeed, SeekSeconds: fl.demoSeek})
		events, err := source.Mux(ctx, src)
		if err != nil {
			fmt.Fprintf(stderr, "agentpane: %v\n", err)
			return 1
		}
		uiCfg.Events = dbg.tee(events)
		uiCfg.Demo = src
		uiCfg.SourceName = "fake"
		uiCfg.Sessions = demoSessionRows
	} else {
		// Live attach: --session pins exactly one id; otherwise the §3.8a
		// rule — newest live session whose cwd matches, else newest live
		// session anywhere (noted), else the idle screen keeps watching the
		// registry.
		regDir, regErr := registry.DefaultDir()
		if regErr != nil {
			fmt.Fprintf(stderr, "agentpane: cannot locate the session registry: %v\n", regErr)
			return 1
		}
		reg := registry.New(regDir)
		cwd, _ := os.Getwd()

		choice := resolveAttach(reg.Sessions(), cwd, fl.session)
		if choice.Note != "" {
			warnings = append(warnings, config.Warning{Msg: choice.Note})
		}

		att := &attacher{appCtx: ctx, out: make(chan event.Event, 256), regDir: regDir, fl: fl, dbg: dbg}
		if err := att.attach(choice.Session); err != nil { // zero Session = registry-only watch
			fmt.Fprintf(stderr, "agentpane: %v\n", err)
			return 1
		}
		switch {
		case choice.Pinned:
			// Pin the machine to the id we were TOLD to watch, before any
			// event arrives — including while the registry row is still
			// missing, so a sibling session's rows can never build a world
			// here.
			machine.Reset(fl.session)
			if choice.Waiting {
				go waitForPinned(ctx, reg, att, fl.session, registryPollInterval)
			}
		case choice.Session.SessionID != "":
			// Same pin for a guessed attach: another session's registry rows
			// (SessionIdle, permission waits) must not mutate this world
			// before our own SessionStart arrives.
			machine.Reset(choice.Session.SessionID)
		}

		// Both on-disk sinks are keyed by the session this pane watches. A
		// pinned pane must never show — or be pruned by — another session, and
		// with N panes running there is no such thing as "the" state file:
		// runs/s-<id>/ and current-<id>.json are per session, so neither the
		// idle screen's history nor --oneline can drift onto a sibling.
		scope := fl.session
		if scope == "" {
			scope = choice.Session.SessionID
		}
		stateDir, _ := statefile.DefaultDir()
		runsDir, _ := runstore.DefaultDir()
		store := &runstore.Store{Dir: runsDir, HistoryRuns: cfg.HistoryRuns, Session: scope}
		writer := &statefile.Writer{Dir: stateDir, Session: scope}
		defer writer.Flush()

		uiCfg.Events = att.out
		uiCfg.StateWriter = writer
		uiCfg.RunStore = store
		uiCfg.SourceName = sourceName(fl)
		uiCfg.Sessions = sessionRows(reg, att)
		uiCfg.PinnedSession = fl.session
		uiCfg.AttachedSession = att.current
		if fl.session == "" {
			// Only an unpinned TUI may switch sessions (§3.8a picker). A
			// pinned pane belongs to one tab; leaving this nil means even a
			// stray key press cannot move it.
			uiCfg.SwitchSession = func(row ui.SessionRow) {
				for _, s := range reg.Sessions() {
					if s.SessionID == row.ID {
						// Re-key both sinks before the swap: the Model reloads
						// run history straight after this returns, and the next
						// state-file write must land under the new session's
						// name, not the old one's. Called from bubbletea's
						// Update goroutine, the same one that calls
						// Store.Append and RunHistory, so the store field needs
						// no lock; the Writer has a trailing timer of its own
						// and takes its lock in SetSession.
						store.Session = s.SessionID
						writer.SetSession(s.SessionID)
						att.attach(s) //nolint:errcheck // degrade silently (§5.4)
						return
					}
				}
			}
		}
		uiCfg.DetailUnavailable = detailUnavailable(fl, cwd)
	}
	uiCfg.Warnings = warnings

	p := tea.NewProgram(ui.New(uiCfg),
		tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "agentpane: %v\n", err)
		return 1
	}
	return 0
}

// registryPollInterval is how often the pinned-session wait re-reads the
// registry; it matches registry.Source's own poll cadence.
const registryPollInterval = time.Second

// attachChoice is the resolved answer to "which session does this TUI
// watch?" for one registry snapshot.
type attachChoice struct {
	Session registry.Session // zero when nothing can be attached yet
	Found   bool             // Session is a real row
	Pinned  bool             // --session was given: the answer can never change
	Waiting bool             // pinned, but that id is not in the registry yet
	Note    string           // config warning to surface once ("" = unambiguous)
}

// resolveAttach is the §3.8a attach rule plus the --session pin, as a pure
// function of one registry snapshot, the process's cwd, and the pinned id
// ("" = no pin). Pure so the no-fallback guarantee is testable without a
// registry directory.
//
// Pinned: only an exact SessionID match attaches. A missing id WAITS. It
// never falls back to the cwd guess — autopane opens the pane DURING
// SessionStart, so the registry file routinely lands a moment after the pane
// does, and guessing during that window would attach the pane to a sibling
// session in the same directory and then look correct forever. That is the
// whole failure this flag removes.
//
// Unpinned: newest live session whose cwd matches (§3.8a); else the newest
// live session anywhere, noted; else nothing (the idle screen keeps
// watching).
func resolveAttach(rows []registry.Session, cwd, pinned string) attachChoice {
	if pinned != "" {
		for _, row := range rows {
			if row.SessionID == pinned {
				return attachChoice{Session: row, Found: true, Pinned: true}
			}
		}
		return attachChoice{Pinned: true, Waiting: true}
	}
	if best, ok := newestForCWD(rows, cwd); ok {
		return attachChoice{Session: best, Found: true}
	}
	if len(rows) > 0 {
		return attachChoice{Session: rows[0], Found: true,
			Note: "attached " + shortID(rows[0].SessionID) + " - newest live session (none matches this directory)"}
	}
	return attachChoice{}
}

// newestForCWD is registry.AttachTarget's rule applied to an already-read
// snapshot: newest live session whose cwd matches, ties broken by pid.
func newestForCWD(rows []registry.Session, cwd string) (registry.Session, bool) {
	cwd = filepath.Clean(cwd)
	var best registry.Session
	found := false
	for _, row := range rows {
		if filepath.Clean(row.CWD) != cwd {
			continue
		}
		if !found || row.UpdatedAt.After(best.UpdatedAt) ||
			(row.UpdatedAt.Equal(best.UpdatedAt) && row.PID > best.PID) {
			best, found = row, true
		}
	}
	return best, found
}

// waitForPinned polls the registry until pinned appears, then attaches the
// real source stack to it. No timeout and no fallback by design: the pane
// belongs to that session's tab, and the honest idle screen ("waiting for
// session <id>") is a better answer than a plausible wrong one. Cancelled
// with the app context.
func waitForPinned(ctx context.Context, reg *registry.Source, att *attacher, pinned string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c := resolveAttach(reg.Sessions(), "", pinned); c.Found {
				att.attach(c.Session) //nolint:errcheck // degrade silently (§5.4)
				return
			}
		}
	}
}

// sourceName names the merged stream for the `?` overlay.
func sourceName(fl cliFlags) string {
	switch fl.source {
	case "hook":
		return "hooks+registry"
	case "transcript":
		return "transcript+registry"
	case "fake":
		return "fake"
	default:
		return "hooks+transcript+registry"
	}
}

// detailUnavailable renders the §1.5 honesty line: with no hooks installed
// and no transcript directory for this cwd, subagent detail cannot exist.
func detailUnavailable(fl cliFlags, cwd string) string {
	if fl.source == "hook" || fl.source == "transcript" {
		return "" // a forced source is a deliberate choice; report nothing
	}
	if hooksInstalled() {
		return ""
	}
	if _, err := os.Stat(projectDirFor(cwd)); err == nil {
		return ""
	}
	return "no hooks installed and no transcript for this directory - run agentpane doctor"
}

// attacher owns the source stack behind the ui's fixed events channel, so
// SwitchSession (idle 1-9, §3.8a) can swap sessions without restarting the
// bubbletea program.
type attacher struct {
	appCtx context.Context
	out    chan event.Event
	regDir string
	fl     cliFlags
	dbg    *debugLog

	mu     sync.Mutex
	cancel context.CancelFunc
	sessID string
	gen    int    // attach generation: a superseded pump stops forwarding
	seq    uint64 // monotonic across attaches — see forward
}

// attach cancels any current source stack and starts one for sess. A zero
// Session attaches to the registry alone (idle watching).
func (a *attacher) attach(sess registry.Session) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	a.gen++
	gen := a.gen
	ctx, cancel := context.WithCancel(a.appCtx)
	ch, err := source.Mux(ctx, a.buildSources(sess)...)
	if err != nil {
		cancel()
		return err
	}
	a.cancel = cancel
	a.sessID = sess.SessionID
	go func() {
		for ev := range ch {
			if !a.forward(ctx, gen, ev) {
				return
			}
		}
	}()
	return nil
}

// forward renumbers Seq monotonically across attaches — each Mux restarts its
// counter at 1, and the machine's (AgentID, Kind, Seq) dedupe would silently
// drop the new session's events as duplicates of the old one's — and stops a
// superseded generation's pump from leaking stale events after a switch.
func (a *attacher) forward(ctx context.Context, gen int, ev event.Event) bool {
	a.mu.Lock()
	if gen != a.gen {
		a.mu.Unlock()
		return false
	}
	a.seq++
	ev.Seq = a.seq
	a.mu.Unlock()
	a.dbg.event(ev)
	select {
	case a.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// scope is the session id this pane's sources belong to: the --session pin
// when there is one (it holds even while the pinned row has not reached the
// registry and nothing is attached yet), else the session actually attached,
// else "" — an unpinned pane with nothing to attach to, which watches the
// whole registry because discovering a session is the only thing left for it
// to do.
func (a *attacher) scope(sess registry.Session) string {
	if a.fl.session != "" {
		return a.fl.session
	}
	return sess.SessionID
}

// current returns the attached session id ("" when watching the registry).
func (a *attacher) current() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessID
}

// buildSources assembles the per-session source stack (--source forces one
// agent-event source; the registry rides along as the discovery/confirmer
// channel, DATA-SOURCES §1).
//
// The registry is the one source in the stack that is session-agnostic: it
// emits a row for EVERY live session, including a sibling's "waiting on
// permission". Whenever this pane knows which session it is for, the registry
// is filtered to it here so those rows never enter the process. This is
// defence in depth, not the guarantee — ui.Model gates every event on
// state.Machine.Accepts, which covers the sources that carry no filter and the
// window before this pane knows its session at all.
func (a *attacher) buildSources(sess registry.Session) []event.Source {
	reg := registry.New(a.regDir, registry.WithSessionFilter(a.scope(sess)))
	if sess.SessionID == "" {
		return []event.Source{reg}
	}
	switch a.fl.source {
	case "hook":
		return []event.Source{hooksrc.New(hooksrc.SocketPath(sess.SessionID)), reg}
	case "transcript":
		return []event.Source{transcript.New(projectDirFor(sess.CWD), sess.SessionID), reg}
	default:
		return []event.Source{
			hooksrc.New(hooksrc.SocketPath(sess.SessionID)),
			transcript.New(projectDirFor(sess.CWD), sess.SessionID),
			reg,
		}
	}
}

// stateConfig maps the PART 7 config onto the machine's §2.5/§3.5 knobs.
func stateConfig(cfg config.Config) state.Config {
	sc := state.DefaultConfig()
	if cfg.StuckAfter > 0 {
		sc.StuckAfter = cfg.StuckAfter
	}
	if cfg.LongRunningAfter > 0 {
		sc.LongRunningAfter = cfg.LongRunningAfter
	}
	if cfg.LongRunningAllow != nil {
		sc.LongRunningAllow = cfg.LongRunningAllow
	}
	sc.WatchNeverExempt = cfg.WatchNeverExempt
	if cfg.DecayAfter > 0 {
		sc.DecayAfter = cfg.DecayAfter
	}
	return sc
}
