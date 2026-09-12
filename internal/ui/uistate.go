package ui

import (
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/slug"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/term"
)

// selBand is the pseudo-selection id for the alert band: ⏎ on it jumps focus
// to the left pane instead of opening the inspector (§5.1).
const selBand = "!band"

// selFinished is the pseudo-selection id for the returned-agents line. The
// tree hides settled subagents by default — a finished agent answers none of
// the three questions the pane exists for, and a run of forty leaves the live
// ones scrolled off the bottom — so the line states how many are held back and
// ⏎ (or `a`) shows them.
const selFinished = "!finished"

// selQuiet is the pseudo-selection id for the suppressed-agents line
// (state.World.Quiet). Selecting it expands the list, so agents kept off the
// tree are one keystroke from being named rather than invisible (§1.5).
const selQuiet = "!quiet"

// SessionRow is one row of the idle screen's session list (§3.8a). It is a
// local type so the CLI stage can adapt internal/source/registry (or the
// demo's fake.DemoSessions) without ui importing either.
type SessionRow struct {
	ID       string
	ShortID  string
	Dir      string
	Live     bool
	Idle     bool
	Attached bool
	Agents   int
	EndedAgo time.Duration
}

// RunAgentRow is one agent bar of a persisted run summary (§3.8).
type RunAgentRow struct {
	Name     string
	Duration time.Duration
	Status   string
	Tokens   int
}

// RunRow is the run the idle screen summarises: the one this pane just
// watched end (§3.8). History browsing is gone — the pane reports the session
// it is attached to, and the runs before this one are `agentpane runs`.
type RunRow struct {
	ID string
	// Label is what the run was FOR — the prompt that started it, one line of
	// it. The id names the record on disk; this names the work, and the work is
	// what a reader recognises.
	Label       string
	Start       time.Time
	End         time.Time
	Agents      []RunAgentRow
	Tokens      int
	Outcome     string
	Stalls      int
	Contentions int
}

// toast is the §5.2 footer-left notice: 3s, newest wins.
type toast struct {
	Text    string
	At      time.Time
	Refusal bool // ⚠ styling instead of ✓
}

const toastFor = 3 * time.Second

// digestEntry is one §3.19 away-digest line.
type digestEntry struct {
	Slug string
	Kind string // "asked permission" | "stalled" | "returned" | "changed"
	When string
	rank int // asks → stalls → returns → other
}

// digest is the materialised away-digest, built on focus-in (§3.19).
type digest struct {
	Dur     time.Duration
	Entries []digestEntry
	More    int
	Marks   map[string]bool // agent ids that changed while away (▌ gutter)
}

// UIState is the view-side state the pure render core consumes alongside the
// state.World snapshot. The Model owns and mutates it; renderFrame only reads
// it (except the idempotent slug-table Assign).
type UIState struct {
	// Layout.
	WidthMode string // "fill" (the pane, up to MaxWidth) | "wide" (64) | "narrow" (44), cycled with w (§3.1)
	// MaxWidth is where fill stops (config max_width). 0 means the default.
	MaxWidth int
	RightCol string // "since" | "rate" (§5.1 t)

	// Selection and expansion (§3.3, §3.17).
	SelID     string
	Pins      map[string]int // agent id → pin sequence (oldest-first drop)
	PinSeq    int
	ScrollTop int // first visible subagent index when scrolling (§3.9)
	// ReturnedTop is the first visible index of the returned-agents list at
	// the foot of the tree. The list is capped at maxReturnedRows however
	// tall the pane is and scrolls under j/k like the tree above it.
	ReturnedTop int

	// Inspector (PART 4).
	InspectorOpen   bool
	InspectorScroll int // lines up from the bottom of the log
	Follow          bool

	// Alerting chrome.
	Toast   toast
	Overlay bool

	// Away-digest (§3.19).
	Away      bool
	AwayStart time.Time
	BlurRev   uint64
	Digest    *digest

	// Idle screen (§3.8, §3.8a).
	Sessions []SessionRow

	// ShowFinished lifts the returned-agent filter (§3.5 deviation, `a`).
	// Off by default: the tree is for work in flight.
	ShowFinished bool

	// Marks is when this pane first PAINTED each fact it animates — an agent
	// arriving, an agent returning, a call landing (anim.go).
	//
	// It is view state, on a view clock, for one reason: the content clock
	// jumps to event timestamps, and the sources deliver in bursts. A
	// transcript poll hands over three seconds of events at once, the clock
	// leaps to the newest, and every animation window opens and closes before
	// the frame is drawn. What is being animated is a row appearing HERE, so
	// the only clock that can measure it is this pane's own.
	Marks map[string]time.Time

	// Anim turns the transitions on. Off by default so a bare UIState — every
	// snapshot test, every direct renderFrame call — draws finished frames; the
	// Model turns it on for the live pane and leaves it off for --render-once.
	Anim bool
	// Wall is that view clock: real time in a live pane, the content clock
	// under --demo so a replay stays deterministic.
	Wall time.Time

	// Clock is the instant the frame is being drawn for, stamped by
	// renderFrame. The filter reads it to hold a just-returned agent on the
	// tree while it moves to the list (anim.go).
	Clock time.Time

	// PinnedSession is the session id this pane is fixed to (--session);
	// "" means the attachment was guessed and may be switched. Attached is
	// the id the source stack currently has, "" while a pinned session has
	// not reached the registry yet.
	PinnedSession string
	Attached      string

	// Data companions.
	Logs  *EventLog
	Slugs *slug.Table

	// Config-derived knobs.
	NotifyAfter time.Duration // §3.20 level 4; the ask pressure bar fills by it
	MaxCalls    int
	DigestCap   int
	SparkMetric string
	LinksOn     bool

	// Doctor/overlay strings.
	SourceName   string
	AccentReason string
	Version      string
	Warnings     []config.Warning

	// Degradations.
	DetailUnavailable string // §1.5 / §3.9 detail-unavailable reason
	DisconnectedAt    time.Time
	HeldOrder         []string // agent order frozen during compaction (§3.9)

	// Demo controls (§1.4, §6.4).
	DemoAvailable bool
	DemoPaused    bool

	// Quit guard (§5.1 ctrl-c).
	QuitArmed bool

	// Emitter renders OSC 8 hyperlinks in the inspector footer (§5.4). May
	// be nil (plain text).
	Emitter *term.Emitter
}

// newUIState builds view state from config defaults.
func newUIState(cfg config.Config) *UIState {
	v := &UIState{
		WidthMode: cfg.Width,
		MaxWidth:  cfg.MaxWidth,
		// The right column defaults to the token burn rate. Time-since-last-
		// event answers "has this gone quiet?", which the stall machinery
		// already shouts about on its own; the rate answers "what is this
		// agent doing right now?", which is the question a row full of a
		// resetting stopwatch could not. `t` swaps back (§5.1, §3.18).
		RightCol:    "rate",
		SelID:       event.MainAgentID,
		Pins:        map[string]int{},
		Marks:       map[string]time.Time{},
		Logs:        NewEventLog(),
		Slugs:       slug.New(cfg.SlugMax),
		NotifyAfter: cfg.NotifyAfter,
		MaxCalls:    cfg.MaxCallsShown,
		DigestCap:   cfg.DigestCap,
		SparkMetric: cfg.Spark,
		LinksOn:     cfg.Links,
	}
	switch v.WidthMode {
	case "narrow", "wide":
	default:
		v.WidthMode = "fill"
	}
	if v.MaxWidth < 44 {
		v.MaxWidth = fillMaxColumns
	}
	if v.MaxCalls <= 0 {
		v.MaxCalls = 4
	}
	if v.DigestCap <= 0 {
		v.DigestCap = 3
	}
	return v
}

// sessionPicker reports whether the §3.8a session list and its 1–9 switch
// keys exist at all. They do ONLY when the attachment is both changeable and
// genuinely ambiguous: not pinned (--session fixes the pane to one tab's
// session) and more than one session to choose between.
//
// Deliberate deviation from SPEC §3.8a, which lists the sessions
// unconditionally: with several sessions per directory the picker on a
// pinned pane would advertise a switch that must never happen. It survives
// for the hand-run `agentpane` in a busy directory, which is the only place
// it is a recovery path rather than a trap.
func (v *UIState) sessionPicker() bool {
	return v.PinnedSession == "" && len(v.Sessions) > 1
}

// pinnedWaiting reports the honest gap between "this pane was opened for
// session X" and "X exists in the registry": autopane splits the pane during
// SessionStart, so the row can arrive a moment later. Nothing is attached
// meanwhile, and the idle screen says so by name rather than guessing.
func (v *UIState) pinnedWaiting() bool {
	return v.PinnedSession != "" && v.Attached == ""
}

// slugFor assigns/returns the stable slug for an agent (§3.16). Teammates
// show their given name verbatim (§3.15). An agent whose name has not arrived
// yet gets a placeholder and reserves nothing (see placeholderName).
func (v *UIState) slugFor(a state.Agent) string {
	if a.Teammate && a.Name != "" {
		return a.Name
	}
	if v.Slugs == nil {
		return a.ID
	}
	if a.Name == "" {
		return placeholderName(a.ID, a.Type)
	}
	// Exactness must travel with every namer, not just the full tree's
	// assignSlugsInSpawnOrder: the condensed, tiny, idle and band paths reach
	// slugFor directly, and a plain Assign would pin a wrongly-correlated name
	// for the rest of the run on exactly the geometries with least room to
	// notice.
	if a.NameExact {
		return v.Slugs.AssignExact(a.ID, a.Name)
	}
	return v.Slugs.Assign(a.ID, a.Name)
}

// slugByID slugs an arbitrary agent id using a name hint (band, contention).
func (v *UIState) slugByID(id, name string) string {
	if id == event.MainAgentID {
		return event.MainAgentID
	}
	if v.Slugs == nil {
		return id
	}
	if name == "" {
		return placeholderName(id, "")
	}
	return v.Slugs.Assign(id, name)
}

// slugOf names an agent referenced from somewhere other than its own row (the
// alert band, a contention line). It prefers the rendered agent, so a row and
// the band that points at it can never disagree about what it is called, and
// falls back to the id-plus-hint form when the agent is not on the tree.
func (v *UIState) slugOf(w state.World, id, name string) string {
	if a, ok := agentByID(w, id); ok {
		return v.slugFor(a)
	}
	return v.slugByID(id, name)
}

// placeholderName is what an agent is called before its description arrives.
//
// It is deliberately NOT a slug: nothing is reserved in the slug table, so the
// real description still gets its own §3.16 slug the moment any source
// supplies one (hooks correlate late, and the transcript sidecar can lag the
// SubagentStart by a poll). Feeding "" through the slug table instead is what
// produced the "agent", "agent-2" … "agent-37" rows — a rename queue's worth
// of identical, meaningless names, numbered by a collision rule that says
// nothing about the agent.
//
// The placeholder is the agent's own reported agent_type plus a discriminator
// derived from its id: both come from the platform, it is unique without a
// counter, and it can never be crossed with another agent's identity (C8).
//
// The discriminator is a digest of the WHOLE id, not a prefix of it. Platform
// ids share long heads — the phantom ids of one captured session differed only
// in their last two characters, and "a" plus three hex digits left 4096 buckets
// for the ~40 announced agents of a single run, a 17% chance that two rows of
// the expanded quiet list were literally the same string. A digest spreads the
// whole id over 36^placeholderDigestLen values instead, so entries of one run
// are unique in practice while staying stable per id across frames.
func placeholderName(id, typ string) string {
	base := slugToken(typ)
	if base == "" {
		base = "agent"
	}
	if id == "" {
		return base
	}
	return base + "-" + idDigest(id)
}

// placeholderDigestLen is the discriminator length in base-36 digits: 36^6 ≈
// 2.2e9 values, so a run announcing 40 agents collides with probability ~4e-7.
const placeholderDigestLen = 6

const digestAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// idDigest hashes an agent id into placeholderDigestLen lowercase base-36
// characters. FNV-1a is not a security primitive and is not used as one: it is
// picked for being stdlib, allocation-free and uniform over short ASCII ids.
func idDigest(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id)) //nolint:errcheck // hash.Hash never returns an error
	v := h.Sum64()
	out := make([]byte, placeholderDigestLen)
	for i := placeholderDigestLen - 1; i >= 0; i-- {
		out[i] = digestAlphabet[v%uint64(len(digestAlphabet))]
		v /= uint64(len(digestAlphabet))
	}
	return string(out)
}

// slugToken lowercases and strips anything outside [a-z0-9] from s, matching
// the §3.16 character set so a placeholder cannot smuggle control characters
// or width-changing runes into a row.
func slugToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// assignSlugsInSpawnOrder keeps slug assignment deterministic regardless of
// the current §3.6 render order: descriptions are registered by spawn index
// before any render-order lookup happens.
func (v *UIState) assignSlugsInSpawnOrder(agents []state.Agent) {
	if v.Slugs == nil {
		return
	}
	idx := make([]int, len(agents))
	for i := range agents {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		return agents[idx[a]].SpawnIndex < agents[idx[b]].SpawnIndex
	})
	for _, i := range idx {
		// Unnamed agents are skipped, not registered: assigning now would
		// burn a permanent "agent"/"agent-2" slug on a description that has
		// not arrived yet, and slugs never change once assigned (§3.16).
		if agents[i].Teammate || agents[i].Name == "" {
			continue
		}
		// An exact name (transcript sidecar) may REPLACE a slug derived from a
		// hooks correlation, which matches descriptions to ids heuristically.
		// A stable wrong name is worse than one that changes once.
		if agents[i].NameExact {
			v.Slugs.AssignExact(agents[i].ID, agents[i].Name)
		} else {
			v.Slugs.Assign(agents[i].ID, agents[i].Name)
		}
	}
}

// orderedAgents returns the rows the tree actually draws: the machine's §3.6
// order (except during compaction, when the pre-compaction order is held,
// §3.9), with returned agents filtered out unless ShowFinished is on.
func (v *UIState) orderedAgents(w state.World) []state.Agent {
	return v.live(v.orderedAll(w))
}

// orderedAll is the same order with nothing filtered out — for the consumers
// that must see every agent whatever the tree is drawing. The §3.19 digest is
// one: "returned" is one of the things it reports, and an agent that landed
// while the reader was away is exactly the row the filter hides.
func (v *UIState) orderedAll(w state.World) []state.Agent {
	if w.Session.State != state.SessionCompacting || len(v.HeldOrder) == 0 {
		return w.Agents
	}
	byID := make(map[string]state.Agent, len(w.Agents))
	for _, a := range w.Agents {
		byID[a.ID] = a
	}
	out := make([]state.Agent, 0, len(w.Agents))
	seen := make(map[string]bool, len(v.HeldOrder))
	for _, id := range v.HeldOrder {
		if a, ok := byID[id]; ok {
			out = append(out, a)
			seen[id] = true
		}
	}
	for _, a := range w.Agents {
		if !seen[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

// live drops the agents that have already returned. It is the whole of the
// hiding rule, applied in one place so every screen — tree, condensed, tiny —
// and every consumer of the render order (selection, the scroll window) sees
// the same rows.
//
// Nothing is lost by dropping a row here: the tree lists the returned agents
// at its foot, where they stay selectable and one ⏎ from their inspector. That
// is why this needs no exemption for the selected agent — an agent that returns
// under the cursor moves from the tree to the list, and the cursor goes with it.
func (v *UIState) live(agents []state.Agent) []state.Agent {
	if v.ShowFinished {
		return agents
	}
	out := make([]state.Agent, 0, len(agents))
	for _, a := range agents {
		if a.Status == state.StatusDone && !holding(a, v) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// didSomething reports whether a returned agent left anything behind worth a
// row in the list.
//
// The list is what the run FINISHED, and Claude Code announces subagents that
// never do a thing — no description, no tool call, no tokens, gone in 0:00. The
// suppression rule (§1.5, state.Machine.quiet) catches the ones with no spawn
// record at all; these have one, so they earn a row while they are alive and
// nobody would argue with that. Once returned they are just names in a list of
// work, next to agents that did some, and they are not the same thing.
//
// They are not dropped silently: emptyReturns counts them and the list header
// says so.
func didSomething(a state.Agent) bool {
	return a.Name != "" || a.TotalCalls > 0 || a.Tokens > 0 ||
		a.LinesAdd > 0 || a.LinesDel > 0 || a.LastError != ""
}

// emptyReturns counts the returned agents that left nothing behind.
func (v *UIState) emptyReturns(w state.World) int {
	n := 0
	for _, a := range w.Agents {
		if a.Status == state.StatusDone && !holding(a, v) && !didSomething(a) {
			n++
		}
	}
	return n
}

// finishedHidden counts the returned agents the filter has taken off the tree.
func (v *UIState) finishedHidden(w state.World) int {
	if v.ShowFinished {
		return 0
	}
	return len(v.returnedAgents(w))
}

// finishedTotal counts every returned agent, hidden or not: what the line says
// while ShowFinished is on.
func finishedTotal(w state.World) int {
	n := 0
	for _, a := range w.Agents {
		if a.Status == state.StatusDone {
			n++
		}
	}
	return n
}

// selectableIDs is the j/k order: every row the frame actually PAINTED, in the
// order it painted them.
//
// It used to be re-derived from the world plus a set of geometry predicates
// (bandPresent, one per pseudo-row), each of them a restatement of
// something the renderer had already decided. Every new row that is drawn only
// sometimes — the returned-agents list is the latest — needed another predicate,
// and any drift between the two put the cursor on a line that was never on
// screen. The frame's own hit-test metadata is the authority: if a row can be
// clicked, it can be selected, and if it was not drawn it is neither.
func selectableIDs(meta *frameMeta) []string {
	if meta == nil {
		return nil
	}
	ids := make([]string, 0, len(meta.lineIDs))
	seen := make(map[string]bool, len(meta.lineIDs))
	for _, id := range meta.lineIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// returnedAgents lists the agents the tree has finished with, most recently
// returned first — the bottom list's contents. Empty while ShowFinished is on:
// there the rows are back in the tree itself.
func (v *UIState) returnedAgents(w state.World) []state.Agent {
	if v.ShowFinished {
		return nil
	}
	var out []state.Agent
	for _, a := range w.Agents {
		// An agent still being held on the tree is not in the list yet: it may
		// not be drawn in both places at once.
		if a.Status == state.StatusDone && !holding(a, v) && didSomething(a) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DoneAt.After(out[j].DoneAt) })
	return out
}

// agentByID looks up a rendered agent.
func agentByID(w state.World, id string) (state.Agent, bool) {
	for _, a := range w.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return state.Agent{}, false
}

// expandable reports whether an agent may expand: decayed, queued, and
// teammate rows never do (§3.3, §3.15).
func expandable(a state.Agent) bool {
	return !a.Decayed && !a.Teammate && a.Status != state.StatusQueued
}

// needsYou reports the §3.10.2a loud role.
func needsYou(a state.Agent) bool {
	return a.Status == state.StatusAsk || a.Status == state.StatusStuck
}
