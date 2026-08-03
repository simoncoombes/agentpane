package narrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// facts is the single place the narrator is allowed to look at the World.
//
// §13.2: "every count in it is DERIVED, never a literal". Keeping the
// derivation in one struct is how that is enforced rather than hoped for — the
// templates below read fields, they never walk World.Agents, so a number in a
// sentence can always be traced to exactly one expression here.
type facts struct {
	w     state.World
	now   time.Time
	names *namer

	runStart time.Time
	runFor   time.Duration
	hasRun   bool

	// Populations, in World order (which is §3.6 render order), so any list of
	// names the prose builds matches the order of the rows above it.
	asks    []state.Ask
	asking  []state.Agent
	stuck   []state.Agent
	live    []state.Agent
	queued  []state.Agent
	done    []state.Agent
	mates   []state.Agent
	unknown []state.Agent

	// openLong are agents holding a tool call longer than openCallLong. A long
	// open call is the one shape of trouble that is NOT stuck: the agent is
	// demonstrably in a call, so silence is explained.
	openLong []state.Agent

	// parked is what "n parked behind that keystroke" counts: agents that cannot
	// proceed until the user answers and are not themselves the thing being
	// answered.
	//
	// It is the QUEUED agents. The asking ones are excluded because they are the
	// keystroke, not a queue behind it: counting them made a lone ask report "1
	// agent is parked behind that keystroke" with nothing whatsoever behind it,
	// and made patience()'s "Nothing else is parked behind it" branch unreachable
	// for as long as any ask existed. It is deliberately NOT stuck either — a
	// stuck agent is waiting on nothing anyone can identify, and folding it in
	// would inflate the one number the escalation ladder leans on.
	//
	// riskLines' "n agents are parked behind that answer" counts len(queued) too,
	// so the two sentences agree by construction rather than by coincidence.
	parked int

	// tokens is the run total; agentTokens is the sum over rows, kept separate
	// so the fallback when there is no run aggregate is visible.
	tokens      int
	agentTokens int
	linesAdd    int
	linesDel    int
	files       int

	// waited is how long the OLDEST outstanding ask has been unanswered, and
	// askShare is that as a fraction of the asking agent's whole life.
	waited   time.Duration
	askShare int
	askAge   time.Duration
}

// openCallLong is when an open tool call stops being "in progress" and starts
// being worth a sentence. It matches the shipped long_running_after default
// rather than inventing a second threshold for prose to use.
const openCallLong = 3 * time.Minute

func newFacts(w state.World, now time.Time, names *namer) *facts {
	f := &facts{w: w, now: now, names: names}

	if w.Run != nil && !w.Run.StartedAt.IsZero() {
		f.runStart, f.hasRun = w.Run.StartedAt, true
	} else {
		// No run aggregate: the earliest thing the world remembers is the
		// honest zero for the commentary's clock. Falling back to `now` would
		// stamp a whole history at 0:00.
		for _, a := range w.Agents {
			if a.SpawnedAt.IsZero() {
				continue
			}
			if f.runStart.IsZero() || a.SpawnedAt.Before(f.runStart) {
				f.runStart = a.SpawnedAt
			}
		}
		if f.runStart.IsZero() {
			f.runStart = now
		}
	}
	if now.After(f.runStart) {
		f.runFor = now.Sub(f.runStart)
	}

	f.asks = w.Asks
	seenFiles := map[string]bool{}
	for _, a := range w.Agents {
		switch a.Status {
		case state.StatusAsk:
			f.asking = append(f.asking, a)
		case state.StatusStuck:
			f.stuck = append(f.stuck, a)
		case state.StatusRun:
			f.live = append(f.live, a)
		case state.StatusQueued:
			f.queued = append(f.queued, a)
		case state.StatusDone:
			f.done = append(f.done, a)
		case state.StatusTeammate:
			f.mates = append(f.mates, a)
		default:
			f.unknown = append(f.unknown, a)
		}
		if a.OpenCall != nil && !a.OpenCall.Since.IsZero() &&
			a.Status != state.StatusDone && now.Sub(a.OpenCall.Since) >= openCallLong {
			f.openLong = append(f.openLong, a)
		}
		f.agentTokens += a.Tokens
		f.linesAdd += a.LinesAdd
		f.linesDel += a.LinesDel
		for _, p := range a.EditedFiles {
			if !seenFiles[p] {
				seenFiles[p] = true
				f.files++
			}
		}
	}
	f.parked = len(f.queued)
	// The run aggregate is the authority on the total when it exists (§2.8): it
	// counts main plus every agent including the ones that have already decayed
	// off the tree, so summing rows would under-report a long run.
	if w.Run != nil && w.Run.Tokens > 0 {
		f.tokens = w.Run.Tokens
	} else {
		f.tokens = f.agentTokens + w.Main.Tokens
	}

	if len(f.asks) > 0 {
		a := f.asks[0]
		if !a.RaisedAt.IsZero() && now.After(a.RaisedAt) {
			f.waited = now.Sub(a.RaisedAt)
		}
		if ag, ok := f.agent(a.AgentID); ok && !ag.SpawnedAt.IsZero() && now.After(ag.SpawnedAt) {
			f.askAge = now.Sub(ag.SpawnedAt)
			if f.askAge > 0 {
				f.askShare = int(f.waited * 100 / f.askAge)
				if f.askShare > 100 {
					f.askShare = 100
				}
			}
		}
	}
	return f
}

func (f *facts) agent(id string) (state.Agent, bool) {
	for _, a := range f.w.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return state.Agent{}, false
}

// scene picks the situation. Asks outrank everything (they are the only thing
// that stops the run); then anything worth watching; then all clear; and an
// empty world is its own case rather than a false "all clear".
func (f *facts) scene() Scene {
	switch {
	case len(f.asks) > 0 || len(f.asking) > 0:
		return SceneAlert
	case len(f.stuck) > 0 || f.liveContention() || len(f.openLong) > 0:
		return SceneWatch
	case len(f.w.Agents) == 0 && f.w.Main.Activity == "" && f.w.Main.Title == "":
		return SceneEmpty
	default:
		return SceneClear
	}
}

// allAsksResolving reports whether every outstanding ask has already been
// answered as far as the events can tell (§3.7.8). An asking AGENT with no ask
// record — the scene is reachable that way — is not resolving, so it holds the
// keys on the action line.
func (f *facts) allAsksResolving() bool {
	if len(f.asks) == 0 || len(f.asking) > len(f.asks) {
		return false
	}
	for _, a := range f.asks {
		if !a.Resolving {
			return false
		}
	}
	return true
}

// liveContention reports whether any recorded collision still has a participant
// running. A collision whose writers have all returned is history: the damage
// either happened or it did not, and there is nothing left to watch. Treating it
// as live is what kept the chip on WORTH WATCHING through an otherwise finished
// run — the standup still SAYS what survived (riskLines' luck-vs-competence
// line), it just stops shouting about it.
func (f *facts) liveContention() bool {
	for _, c := range f.w.Contentions {
		if f.hasLiveWriter(c.AgentIDs) {
			return true
		}
	}
	return false
}

// hasLiveWriter is the SAME classification splitWriters uses, so the scene, the
// closing action and the risk sentence cannot disagree about whether a collision
// still has a live participant — the action promising a writer to inspect while
// the risk line has none to name would be the §13.2 action pointing at nothing.
//
// It resolves no names on purpose: scene() runs before any sentence is built, and
// asking the namer here would mark every contention participant as one this frame
// refers to (see namedIn) whether or not it is ever mentioned.
func (f *facts) hasLiveWriter(ids []string) bool {
	for _, id := range ids {
		if !f.writerReturned(id) {
			return true
		}
	}
	return false
}

// writerReturned is that classification for ONE participant. A writer the World no
// longer carries is not returned: the events do not say it came back, so neither
// the scene nor the prose may.
func (f *facts) writerReturned(id string) bool {
	a, ok := f.agent(id)
	return ok && a.Status == state.StatusDone
}

// since renders an elapsed duration the way the rest of the pane does (§2.6),
// so a duration in a sentence and the same duration on a row read identically.
func since(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
	}
}

// clock renders m:ss (§3.12).
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// tokensText renders a token total; zero renders empty (honest absence, C8).
func tokensText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d tokens", n)
	case n < 1000000:
		return fmt.Sprintf("%dk tokens", (n+500)/1000)
	default:
		return fmt.Sprintf("%.1fM tokens", float64(n)/1000000)
	}
}

// plural is the only pluralisation helper; every count in the prose goes
// through it so "1 agents" cannot happen.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// countOf renders "1 agent" / "3 agents".
func countOf(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, plural(n, noun, noun+"s"))
}

// nameList renders up to max slugs as an English list, then "+n more". The cap
// exists because a sentence naming nine agents is a table with commas in it.
func nameList(names []string, max int) string {
	if len(names) == 0 {
		return ""
	}
	rest := 0
	if len(names) > max {
		rest = len(names) - max
		names = names[:max]
	}
	var s string
	switch len(names) {
	case 1:
		s = names[0]
	case 2:
		s = names[0] + " and " + names[1]
	default:
		s = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	if rest > 0 {
		s += fmt.Sprintf(" (+%d more)", rest)
	}
	return s
}

func (f *facts) slugsOf(agents []state.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, f.names.of(a))
	}
	return out
}

// askGist names what an ask needs WITHOUT reprinting the command. It is the form
// EVERY part of a frame uses while the ask is live: the standup's own sentences
// always, and the commentary's log entry for as long as the band is drawing the
// same bytes (Entry.Cmd, Entry.TextOn).
//
// §3.7 gives the exact string to the band's own row, verbatim, because that row
// is what the reader approves. A sentence four rows under it that repeats the
// same string states the ask twice inside one region, which is exactly the
// duplication that merging the band into the standup existed to remove. So the
// prose says what KIND of answer is wanted and leaves the string to the row that
// owns it — the mock does the same thing in its own words ("wants to clear it",
// never the rm -rf).
//
// The invariant is whole-FRAME and not per region: fixing this per region is what
// let the duplication come back twice, in a new pair of regions each time (band +
// mustLines, then band + commentary).
//
// "one" is load-bearing: it says the thing the reader most wants to know that
// the row does not already show, which is that this is a single decision.
func (f *facts) askGist(a state.Ask) string {
	switch a.Kind {
	case "command":
		if a.Tool != "" {
			return "permission to run one " + a.Tool + " command"
		}
		return "permission to run one command"
	case "file_write":
		return "permission to write one file"
	case "network":
		return "network access"
	case "mcp":
		if a.Tool != "" {
			return "permission to use " + a.Tool
		}
		return "permission to use an MCP tool"
	default:
		if a.Tool != "" {
			return "permission to use " + a.Tool
		}
		return "your answer"
	}
}

// askWhat names what an ask is asking for, from the payload kind (§3.7.1),
// quoting the command verbatim. It is the COMMENTARY's RECORD form: a log entry
// is a timestamped record of what was asked, it is the only place the approved
// bytes survive once the band has cleared, and it is what keeps stripQuoted's job
// real (a token inside a verbatim command is a shell token, not an agent).
//
// It is not printed while the band is showing the same bytes — see Entry.Cmd for
// the ruling and Entry.TextOn for where it is applied. The standup never uses it
// at all; askGist is its form. See it for why.
//
// The command is whitespace-collapsed, exactly as quoteCmd and the band's own
// display copy (§13.1(b)) collapse it, so "the bytes the frame is showing" is one
// string and not three spellings of one.
func (f *facts) askWhat(a state.Ask) string {
	cmd := normCmd(a.Command)
	switch a.Kind {
	case "command":
		if cmd != "" {
			return "permission to run " + quoteCmd(cmd)
		}
		return "permission to run a command"
	case "file_write":
		if cmd != "" {
			return "permission to write " + cmd
		}
		return "permission to write a file"
	case "network":
		return "network access"
	case "mcp":
		if a.Tool != "" {
			return "permission to use " + a.Tool
		}
		return "permission to use an MCP tool"
	default:
		if a.Tool != "" && cmd != "" {
			return "permission for " + a.Tool + " " + quoteCmd(cmd)
		}
		if a.Tool != "" {
			return "permission to use " + a.Tool
		}
		return "your answer"
	}
}

// cmdCap bounds a quoted command inside a sentence. Longer than this and the
// sentence stops being readable; the exact bytes are one `y` away, and the alert
// band already prints the command in full on its own line.
const cmdCap = 44

func quoteCmd(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > cmdCap {
		s = strings.TrimRight(string(r[:cmdCap-1]), " ") + "…"
	}
	return "`" + s + "`"
}

// namer resolves ids to display slugs once per frame and remembers the order it
// saw them in, so Result.Named is stable.
//
// It also records WHICH ids the composer asked about. That set is the honest
// answer to "whose name does this frame refer to" — deriving it from the rendered
// text alone cannot distinguish an agent's slug from the same characters
// appearing inside another slug or inside a quoted command, and Result.Named
// freezes every id it reports. See namedIn.
type namer struct {
	byID  map[string]string
	used  map[string]bool
	order []string
	fn    func(state.Agent) string
}

func newNamer(w state.World, fn func(state.Agent) string) *namer {
	n := &namer{
		byID: make(map[string]string, len(w.Agents)),
		used: make(map[string]bool, len(w.Agents)),
		fn:   fn,
	}
	for _, a := range w.Agents {
		n.order = append(n.order, a.ID)
		n.byID[a.ID] = n.resolve(a)
	}
	return n
}

func (n *namer) resolve(a state.Agent) string {
	if n.fn != nil {
		if s := n.fn(a); s != "" {
			return s
		}
	}
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// of names an agent from its own record. Asking is what marks the id as one this
// frame refers to (see namer's doc comment).
func (n *namer) of(a state.Agent) string {
	n.used[a.ID] = true
	if s, ok := n.byID[a.ID]; ok && s != "" {
		return s
	}
	return n.resolve(a)
}

// hinted names an agent referred to from somewhere other than its own row (an
// ask, a contention), falling back to a supplied hint.
func (n *namer) hinted(id, hint string) string {
	n.used[id] = true
	if s, ok := n.byID[id]; ok && s != "" {
		return s
	}
	if hint != "" {
		return hint
	}
	return id
}
