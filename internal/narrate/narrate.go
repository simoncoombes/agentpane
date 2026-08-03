// Package narrate turns a state.World snapshot into prose: the standup block
// and the commentary log of SPEC §13.2.
//
// Three properties define the package and every design decision in it follows
// from one of them.
//
// **No model call.** Every sentence is a local template filled from event data.
// The point of the region is that it is always current and always free; a
// round-trip to a model would make it neither, and would put an inference
// engine between the events and the reader in a pane whose entire promise is
// that it only says what it saw.
//
// **Pure.** Narrate is a function of (World, prior Memory, now). No clock, no
// I/O, no globals, no map iteration reaching the output. The same World and the
// same Memory produce byte-identical text, which is what makes the region
// snapshot-testable without a terminal and what stops the prose from flickering
// between two phrasings on a 10fps repaint.
//
// **Honest.** DATA-SOURCES is blunt about what the platform does not emit:
// there is no subagent failure signal (§1 caveat 3), grant/deny has no hook
// (caveat 1), and a retried ask arrives under a fresh agent id (caveat 1). So a
// sentence here may report a transition, count something, or subtract two
// timestamps — and when it goes further than that it is built through infer(),
// which appends one of the hedges below. An unhedged causal claim is a bug, and
// TestEveryInferenceIsHedged is the test that says so.
package narrate

import (
	"sort"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// Part is the fixed sentence order of the standup (§13.2): what you must do,
// what is at risk, then the state — and it always ends on the action.
//
// The order is not stylistic. A pane that leads with state buries the one line
// that changes what the reader does next under four that do not, and the reader
// learns to skip the block. Compose emits parts in this order and never
// interleaves them; standupOrdered() is the invariant a test can hold onto.
type Part uint8

const (
	PartMust Part = iota
	PartRisk
	PartState
	PartAction
)

func (p Part) String() string {
	switch p {
	case PartMust:
		return "must"
	case PartRisk:
		return "risk"
	case PartState:
		return "state"
	default:
		return "action"
	}
}

// Tone is the render weight a line asks for. It is deliberately NOT a colour:
// internal/ui owns the palette (§3.10) and maps these onto the same four styles
// the rest of the pane uses, so narration cannot introduce a fifth semantic
// state through the back door.
type Tone uint8

const (
	ToneQuiet Tone = iota // deepest chrome: totals, bookkeeping
	ToneDim               // settled facts
	ToneBody              // primary text: the sentence that matters
	ToneWatch             // worth watching
	ToneAlert             // needs you
)

// Scene is which of the three §13.2 situations the world is in. It picks the
// chip and the shape of the standup, and nothing else.
type Scene uint8

const (
	SceneEmpty Scene = iota // nothing has reported yet
	SceneClear
	SceneWatch // something is worth watching, nothing is blocked on you
	SceneAlert // an ask is outstanding
)

// Chip strings — the sticky top row of the standup region. They reuse the
// alert band's own glyphs (§3.11) so the two regions cannot appear to disagree
// about how bad things are.
const (
	ChipAlert = "⚑ NEEDS YOU"
	ChipWatch = "⚠ WORTH WATCHING"
	ChipClear = "✔ ALL CLEAR"
	ChipEmpty = "◌ NOTHING YET"
)

// Hedges. An inferred sentence is built by infer(), which appends one of these,
// so the marking cannot be forgotten at a call site (§3.14: mark inference
// everywhere).
const (
	hedgeUnproven = "nothing here proves it"
	hedgeNoSignal = "the platform doesn't emit the signal that would settle it"
	hedgeGuess    = "that's a guess, not a reading"
	hedgeCantTell = "I can't tell which from here"
	hedgeOrdering = "the ordering saved us, not any actual coordination"
)

// hedges is the closed set. A test walks every scene of every fixture world and
// asserts that each Inferred line ends in one of them.
var hedges = []string{hedgeUnproven, hedgeNoSignal, hedgeGuess, hedgeCantTell, hedgeOrdering}

// Hedges returns the closed hedge set for tests and for anything that wants to
// verify a line marks itself.
func Hedges() []string { return append([]string(nil), hedges...) }

// Line is one standup sentence.
type Line struct {
	Part Part
	Tone Tone
	Text string
	// Inferred marks a sentence that goes further than the events do. Its text
	// always ends in a hedge — infer() is the only constructor.
	Inferred bool
	// Mine marks the narrator correcting itself: an earlier line that the state
	// has since contradicted (§13.2 "that one's mine").
	Mine bool
}

// Entry is one commentary line: a timestamped record of a transition that
// deserved a sentence. See Observe for the filter.
type Entry struct {
	// Key is the dedupe identity. An entry is appended at most once per key for
	// the life of the Memory, which is what makes "appended once per event" true
	// even though Narrate re-derives every candidate from the World on every
	// single frame.
	Key string
	// At is the entry's OWN event time, measured from the run start — not the
	// time it happened to be noticed. A World observed once at the end of a run
	// therefore produces a correctly-ordered history rather than a pile of
	// entries all stamped "now".
	At       time.Duration
	Tone     Tone
	Text     string
	Inferred bool
	Mine     bool
}

// Standup is the whole standup block: a sticky chip, the ordered sentences, and
// an action that is always present.
type Standup struct {
	Scene Scene
	Chip  string
	Tone  Tone
	// Clock is the run clock, rendered on the chip row.
	Clock string
	// Lines are PartMust then PartRisk then PartState, never interleaved.
	Lines []Line
	// Action closes the block. It is ALWAYS populated: §13.2 requires the
	// standup to end by naming the action or explicitly saying none is needed,
	// so there is no such thing as a standup without one.
	Action Line
}

// All returns the body plus the action, in render order.
func (s Standup) All() []Line { return append(append([]Line(nil), s.Lines...), s.Action) }

// Memory is the narrator's carry-over between frames. Treat it as an opaque
// value: Narrate never mutates the Memory it is given and returns the next one,
// so a caller holding an old Memory can re-narrate an old World and get the old
// text back (which is exactly what the determinism test does).
type Memory struct {
	// Entries is the commentary, oldest first, capped at Cap.
	Entries []Entry
	// said is the set of Entry keys already emitted.
	said map[string]bool
	// wroteOff records agents the narrator has publicly called wedged, and
	// when. It is the state that makes self-correction possible: an agent in
	// here that later returns proves the earlier line wrong, and §13.2 requires
	// the narrator to say so.
	wroteOff map[string]time.Duration
	// asking records agents the narrator has announced as blocked on the user,
	// so the resolution can be reported once when they stop.
	asking map[string]bool
	// drops counts source disconnections and compactions counts compaction
	// runs. Both exist so a REPEAT of a session-level event gets its own key: a
	// fixed key would let the second disconnection of a run pass in silence,
	// and a per-frame counter would emit one line per repaint. They are bumped
	// only on the transition INTO the state — see connState/sessState.
	drops       int
	compactions int
	// connState and sessState are tri-state (0 unknown, then the observed
	// value) so the first frame of a session is not mistaken for a transition.
	connState int8
	sessState int8
	// capN is the ring size clone reserved, carried so observe can trim without
	// guessing it from the slice's capacity.
	capN int
}

// Cap bounds the commentary ring. 400 entries is far more than the region can
// ever show and small enough to be free; the oldest are dropped first, which is
// the only direction a log can be trimmed without lying about its start.
const Cap = 400

// Options carries what the narrator cannot derive from the World.
type Options struct {
	// Name resolves an agent to its display slug. internal/ui passes
	// UIState.slugFor so a sentence and the row it refers to always agree.
	// Nil falls back to the agent's own reported description, then its id.
	//
	// Narrate's determinism is conditional on this being deterministic, which
	// the slug table is (§3.16: a slug never changes once assigned).
	Name func(state.Agent) string
	// Cap overrides the commentary ring size; 0 means Cap.
	Cap int
}

// Result is one frame of narration.
type Result struct {
	Standup Standup
	// New are the entries this call appended, oldest first. Callers that need
	// to react to fresh commentary (the scroll-pause bookkeeping in ui) read
	// this rather than diffing Memory.Entries.
	New []Entry
	// Memory is the carry-over for the next call.
	Memory Memory
	// Named lists, in World order, the agent ids this frame's text names.
	//
	// §13.1(a): once a name has appeared in the standup it must never change
	// under the reader, so ui publishes exactly these ids into the slug table
	// and the freeze takes effect from the first frame the name was visible.
	Named []string
}

// Narrate is the whole package: standup plus new commentary for one frame.
func Narrate(w state.World, prior Memory, now time.Time, opt Options) Result {
	mem := prior.clone(opt.cap())
	names := newNamer(w, opt.Name)
	f := newFacts(w, now, names)

	newEntries := observe(f, &mem)
	st := compose(f, mem)

	// Named is derived from the rendered text, not from the facts the composer
	// happened to look at: a name is frozen because the READER saw it, so the
	// test is whether it is in the frame.
	return Result{
		Standup: st,
		New:     newEntries,
		Memory:  mem,
		Named:   namedIn(st, newEntries, names),
	}
}

// Compose is the standup alone, with no commentary work and no Memory to
// return.
//
// The render path calls this on every frame: the standup must describe the frame
// being drawn, but re-deriving the commentary candidates ten times a second only
// to throw them away is pure waste — and a render that cannot append is a render
// that provably does not mutate the log. Memory is still an input because
// self-correction ("that one's mine") needs to know what was already claimed.
func Compose(w state.World, prior Memory, now time.Time, opt Options) Standup {
	return compose(newFacts(w, now, newNamer(w, opt.Name)), prior)
}

func (o Options) cap() int {
	if o.Cap > 0 {
		return o.Cap
	}
	return Cap
}

// clone deep-copies the mutable parts of a Memory so Narrate can write freely
// without touching the caller's value.
func (m Memory) clone(capacity int) Memory {
	out := Memory{
		said:        make(map[string]bool, len(m.said)+8),
		wroteOff:    make(map[string]time.Duration, len(m.wroteOff)+2),
		asking:      make(map[string]bool, len(m.asking)+2),
		drops:       m.drops,
		compactions: m.compactions,
		connState:   m.connState,
		sessState:   m.sessState,
		capN:        capacity,
	}
	for k, v := range m.said {
		out.said[k] = v
	}
	for k, v := range m.wroteOff {
		out.wroteOff[k] = v
	}
	for k, v := range m.asking {
		out.asking[k] = v
	}
	if n := len(m.Entries); n > 0 {
		start := 0
		if n > capacity {
			start = n - capacity
		}
		out.Entries = append(make([]Entry, 0, capacity), m.Entries[start:]...)
	}
	return out
}

// infer builds a sentence that goes further than the events do. The hedge is
// appended here rather than written at the call site so an inference cannot
// ship unmarked (§3.14, C8).
func infer(part Part, tone Tone, claim, hedge string) Line {
	return Line{Part: part, Tone: tone, Text: claim + " — " + hedge + ".", Inferred: true}
}

// inferEntry is infer for the commentary.
func inferEntry(key string, at time.Duration, tone Tone, claim, hedge string) Entry {
	return Entry{Key: key, At: at, Tone: tone, Text: claim + " — " + hedge + ".", Inferred: true}
}

// namedIn collects the agent ids whose slug appears in the frame's text, in
// World order so the result is deterministic.
func namedIn(st Standup, fresh []Entry, names *namer) []string {
	var b strings.Builder
	for _, l := range st.All() {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	for _, e := range fresh {
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
	text := b.String()
	var out []string
	for _, id := range names.order {
		if slug := names.byID[id]; slug != "" && strings.Contains(text, slug) {
			out = append(out, id)
		}
	}
	return out
}

// sortEntries orders candidate entries by event time then key. The key
// tie-break is what makes a batch of same-instant transitions (a fan-out of
// four agents spawned in the same second) render in one fixed order instead of
// whatever order the world happened to be walked in.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].At != es[j].At {
			return es[i].At < es[j].At
		}
		return es[i].Key < es[j].Key
	})
}
