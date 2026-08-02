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

// RunRow is one persisted run for idle-screen history browsing (§2.8, §3.8).
//
// Session is the session that produced the run, "" when the record predates
// session-scoped runs and its provenance is genuinely unknown. It matters
// because an unpinned pane with no live session reads the WHOLE run store —
// every session's subdirectory — so without it three sessions' runs render as
// one pane's history.
type RunRow struct {
	ID          string
	Session     string
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
	WidthMode string // "wide" (64) | "narrow" (44), toggled with w (§3.1)
	RightCol  string // "since" | "rate" (§5.1 t)

	// Selection and expansion (§3.3, §3.17).
	SelID     string
	Pins      map[string]int // agent id → pin sequence (oldest-first drop)
	PinSeq    int
	ScrollTop int // first visible subagent index when scrolling (§3.9)

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
	HistIdx  int
	Sessions []SessionRow
	Runs     []RunRow

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
		WidthMode:   cfg.Width,
		RightCol:    "since",
		SelID:       event.MainAgentID,
		Pins:        map[string]int{},
		Logs:        NewEventLog(),
		Slugs:       slug.New(cfg.SlugMax),
		MaxCalls:    cfg.MaxCallsShown,
		DigestCap:   cfg.DigestCap,
		SparkMetric: cfg.Spark,
		LinksOn:     cfg.Links,
	}
	if v.WidthMode != "narrow" {
		v.WidthMode = "wide"
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
		if !agents[i].Teammate && agents[i].Name != "" {
			v.Slugs.Assign(agents[i].ID, agents[i].Name)
		}
	}
}

// orderedAgents returns the render order: the machine's §3.6 order, except
// during compaction when the pre-compaction order is held (§3.9).
func (v *UIState) orderedAgents(w state.World) []state.Agent {
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

// selectionIDs is the j/k order: the band pseudo-row (when present), main,
// the suppressed-agents line (when it is drawn), then every rendered agent in
// render order — the order the rows are actually painted in.
//
// quietDrawn comes from the geometry, not from len(w.Quiet): the <40-column
// screen states the count in its header instead of as a row, and j/k must
// never land on a line that was never painted.
func (v *UIState) selectionIDs(w state.World, bandPresent, quietDrawn bool) []string {
	ids := make([]string, 0, len(w.Agents)+3)
	if bandPresent {
		ids = append(ids, selBand)
	}
	ids = append(ids, event.MainAgentID)
	if quietDrawn {
		ids = append(ids, selQuiet)
	}
	for _, a := range v.orderedAgents(w) {
		ids = append(ids, a.ID)
	}
	return ids
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
