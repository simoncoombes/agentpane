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
	"unicode"
	"unicode/utf8"

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

// ActionResolving is the closing line for a band whose every ask has been
// answered but whose outcome no event has confirmed (§3.7.8). internal/ui prints
// the same words on the band's own action row (band.go's copyResolvingAction
// aliases this constant), so the two layers cannot drift into telling the reader
// two different things about the same state.
const ActionResolving = "nothing needs you — waiting on the event that confirms it"

// ActionResolvingNarrow is the same state in 41 columns, for a pane that cannot
// hold the long form.
//
// With the `▸ ` prefix the long one is 53 columns and the §3.1 narrow budget is
// 44, so it was cut mid-clause — `▸ nothing needs you — waiting on the event …` —
// which reads as a rendering fault rather than as a state. Both halves of the
// sentence survive here: nothing is needed, and something is still awaited. A
// shorter sentence is a choice; a truncated one is an accident.
const ActionResolvingNarrow = "nothing needs you — awaiting confirmation"

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

	// Cmd and Gist are the two-form rule that keeps a command from being printed
	// twice in one frame. THE RULING (see CommandsOnScreen):
	//
	// The verbatim command belongs to the band's row, because §3.7 requires it
	// there — that row is what the reader approves. The commentary is a LOG: it
	// may keep the exact bytes for the historical record, but not while the same
	// command is on screen in the band. So an entry that quotes a command carries
	// both forms and the RENDERER picks: Gist (which names the kind, as askGist
	// does) while the ask is live and the band is drawing it, Text — the verbatim
	// record — once the ask has resolved and the band no longer shows it.
	//
	// Nothing is rewritten: both forms are fixed when the entry is appended and
	// the Memory keeps the bytes for the life of the run. Only which of the two
	// is PRINTED depends on the frame, which is the only way the log can keep a
	// lasting record of the approved bytes and the frame can still state the
	// command exactly once.
	//
	// Cmd is the whitespace-collapsed command quoted inside Text, and "" for the
	// overwhelming majority of entries, which quote nothing. Gist is empty
	// whenever Cmd is: TextOn then always returns Text.
	Cmd  string
	Gist string
}

// TextOn is the entry's text for a frame whose band is printing the commands in
// onScreen (built by CommandsOnScreen). It is the one place the two-form rule is
// applied, so no caller can print the wrong form by forgetting it.
func (e Entry) TextOn(onScreen map[string]bool) string {
	if e.Cmd != "" && e.Gist != "" && onScreen[e.Cmd] {
		return e.Gist
	}
	return e.Text
}

// CommandsOnScreen is the set of commands the alert band may print verbatim on
// this frame, normalised the way Entry.Cmd is so the two can be compared.
//
// It is derived from the OUTSTANDING asks rather than from the rendered rows.
// That is deliberate: the band's row can be clipped by a short region, and
// paraphrasing a log line whose bytes turned out not to be drawn costs the reader
// nothing (both forms say what was asked), while printing the bytes twice is the
// defect this exists to prevent. Conservative in the safe direction.
func CommandsOnScreen(w state.World) map[string]bool {
	if len(w.Asks) == 0 {
		return nil
	}
	out := make(map[string]bool, len(w.Asks))
	for _, a := range w.Asks {
		if c := normCmd(a.Command); c != "" {
			out[c] = true
		}
	}
	return out
}

// normCmd is the comparison form of a command: whitespace runs collapsed, which
// is what both the band's display copy (§13.1(b) flattening) and quoteCmd do
// before printing it.
func normCmd(s string) string { return strings.Join(strings.Fields(s), " ") }

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

// namedIn collects the agent ids this frame's text actually names, in World order
// so the result is deterministic.
//
// Naming an id here FREEZES its slug for the rest of the run (§13.1(a)), so a
// false positive is not cosmetic: it pins a name the reader never saw and blocks
// the one correction the slug table is allowed to make. A plain substring search
// produced two of them, and the fix is two independent gates that a name has to
// pass:
//
//   - The composer asked for it. namer records every id whose name it handed to a
//     template, so a slug that merely OCCURS in the text — as a fragment of
//     another agent's slug, or as a token inside a verbatim command — is not
//     mistaken for a reference to that agent.
//   - The name is in the frame. `used` alone would over-report: a candidate entry
//     that was already said, or a risk line dropped by riskCap, resolves a name
//     that never reaches the reader.
//
// The two are complementary — one catches "in the text but not referred to", the
// other "referred to but not rendered" — and mentions() supplies the word
// boundaries and the backtick exclusion that make the first gate's text test
// exact rather than approximate.
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
	text := stripQuoted(b.String())
	var out []string
	for _, id := range names.order {
		slug := names.byID[id]
		if slug == "" || !names.used[id] {
			continue
		}
		if mentions(text, slug) {
			out = append(out, id)
		}
	}
	return out
}

// Mentions and StripQuoted are the two text rules namedIn rests on, exported for
// the other half of the same handover: internal/ui decides which of the names the
// narrator produced actually reached the drawn frame (§13.1(a)), and it has to ask
// that question exactly the way this package answered it — whole tokens only, and
// never inside a verbatim command.
func Mentions(text, slug string) bool { return mentions(text, slug) }

// StripQuoted removes backtick-quoted spans from text. See stripQuoted.
func StripQuoted(s string) string { return stripQuoted(s) }

// mentions reports whether slug appears in text as a whole token. A substring hit
// is not a mention: "email-index" contains "email", and freezing the agent called
// email because a DIFFERENT agent's slug happens to contain it names an agent the
// reader never read.
func mentions(text, slug string) bool {
	if slug == "" {
		return false
	}
	for i := 0; i+len(slug) <= len(text); {
		j := strings.Index(text[i:], slug)
		if j < 0 {
			return false
		}
		p := i + j
		if boundedLeft(text, p) && boundedRight(text, p+len(slug)) {
			return true
		}
		i = p + 1
	}
	return false
}

// isSlugRune is the slug alphabet: internal/slug builds a name from unicode
// letters and digits joined with hyphens, and marks a truncation with "…". The
// ellipsis counts as part of the token, which is what keeps the untruncated
// "typecheck-works" from matching inside the truncated "typecheck-works…".
func isSlugRune(r rune) bool {
	return r == '-' || r == '_' || r == '…' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// boundedLeft/boundedRight are \b, evaluated only on the sides where the slug's
// own edge is part of the alphabet — a slug ending in "…" imposes no constraint
// to its right beyond the ellipsis itself.
func boundedLeft(text string, at int) bool {
	if at == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:at])
	return !isSlugRune(r)
}

func boundedRight(text string, at int) bool {
	if at >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[at:])
	return !isSlugRune(r)
}

// stripQuoted removes backtick-quoted spans before the text is searched for
// names. quoteCmd is the only thing in this package that emits a backtick, so
// what comes out is exactly the verbatim command the reader is being asked to
// approve — and a token inside a shell command is a shell token, not a reference
// to an agent that happens to share its name.
//
// Each span leaves a space behind so the token boundaries either side of it
// survive. An unbalanced backtick can only come from a command that contained one
// of its own; the tail is kept rather than swallowing the rest of the sentence.
func stripQuoted(s string) string {
	if !strings.ContainsRune(s, '`') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteByte(' ')
		rest := s[i+1:]
		j := strings.IndexByte(rest, '`')
		if j < 0 {
			b.WriteString(rest)
			return b.String()
		}
		s = rest[j+1:]
	}
}

// mergeEntries splices a batch of newly-emitted entries into an already-ordered
// log, keeping the whole log non-decreasing in At.
//
// Both inputs are sorted, so this is one linear pass. Ties keep the entry that
// was ALREADY in the log first: it was written first, and a stable rule is what
// keeps re-narration byte-identical. The invariant it maintains — Entries is
// ordered by At — is what the renderer's timestamp column depends on, and it
// holds however out of order the underlying events were detected in.
// entryRank orders entries that share a timestamp by what must have happened
// first. Ties are not hypothetical: a phantom subagent is announced and settles
// inside the same second, so without this the log could read "agent returned"
// above "Spawned agent" — an ordering the events contradict. The rank is derived
// from the Key prefix, so no entry has to carry an ordering field.
func entryRank(key string) int {
	switch {
	case key == "run" || strings.HasPrefix(key, "run:"):
		return 0
	case strings.HasPrefix(key, "spawn:"), strings.HasPrefix(key, "mate:"):
		return 1
	case strings.HasPrefix(key, "ask:"):
		return 2
	case strings.HasPrefix(key, "stuck:"):
		return 3
	case strings.HasPrefix(key, "ask-done:"):
		return 4
	case strings.HasPrefix(key, "done:"):
		return 5
	case strings.HasPrefix(key, "mine:"):
		return 6
	case key == "run-quiet":
		return 7 // a summary of the population, so it follows the events
	}
	return 4
}

// before reports whether a sorts ahead of b: by time, then by causal rank.
func before(a, b Entry) bool {
	if a.At != b.At {
		return a.At < b.At
	}
	return entryRank(a.Key) < entryRank(b.Key)
}

func mergeEntries(log, fresh []Entry) []Entry {
	switch {
	case len(fresh) == 0:
		return log
	case len(log) == 0:
		return append(log, fresh...)
	}
	out := make([]Entry, 0, len(log)+len(fresh))
	i, j := 0, 0
	for i < len(log) && j < len(fresh) {
		if before(fresh[j], log[i]) {
			out = append(out, fresh[j])
			j++
			continue
		}
		out = append(out, log[i])
		i++
	}
	out = append(out, log[i:]...)
	out = append(out, fresh[j:]...)
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
