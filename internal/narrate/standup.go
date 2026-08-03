package narrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The waiting ladder (§13.2: "the waiting line escalates with duration and
// every count in it is DERIVED, never a literal").
//
// Four rungs, because each one is a different argument. Under a minute there is
// nothing to argue: prompts take a moment to notice. Past that the cost is
// stated as what it is — one keystroke. Past that the cost is stated as what it
// blocks, counted from the world. And past five minutes the comparison that
// actually stings is the run's own arithmetic, which is why the last rung is
// CHECKED before it is claimed: waitedLongerThanWorked() has to be true or the
// rung falls back to a sentence that only reports the wait.
const (
	patienceCalm   = 60  // seconds: nothing lost yet
	patienceNudge  = 150 // seconds: it's one key
	patienceParked = 300 // seconds: n parked behind it
)

// patience renders the ladder rung for a wait of `waited`, with `parked` agents
// blocked behind it inside a run that has been going `runFor`.
func (f *facts) patience() string {
	sec := int(f.waited / time.Second)
	switch {
	case sec < patienceCalm:
		return "Nothing's lost yet."
	case sec < patienceNudge:
		return "It's one key — y, a or n — and everything downstream of it moves."
	case sec < patienceParked:
		if f.parked <= 0 {
			return "Nothing else is parked behind it, which is the only good news on this screen."
		}
		return fmt.Sprintf("%s %s parked behind that keystroke, which makes it the cheapest fix available to anyone here.",
			countOf(f.parked, "agent"), plural(f.parked, "is", "are"))
	default:
		if f.waitedLongerThanWorked() {
			return fmt.Sprintf("The run's now spent longer waiting on you than doing anything else — %s of %s.",
				since(f.waited), since(f.runFor))
		}
		return fmt.Sprintf("%s on one keystroke, and nothing on this screen is going to move it but you.",
			since(f.waited))
	}
}

// waitedLongerThanWorked is the arithmetic behind the last rung, stated so the
// claim can be audited: the run has spent `waited` on this one prompt out of
// `runFor` total, so the rest of the run — everything that was not this wait —
// is runFor-waited. "Longer waiting than working" is true exactly when the wait
// is more than half the run. It is derived, never assumed, and when it is false
// the sentence is not used.
func (f *facts) waitedLongerThanWorked() bool {
	return f.runFor > 0 && f.waited*2 > f.runFor
}

// compose builds the standup: chip, the ordered sentences, and the closing
// action.
func compose(f *facts, mem Memory) Standup {
	sc := f.scene()
	st := Standup{Scene: sc, Clock: clock(f.runFor)}
	switch sc {
	case SceneAlert:
		st.Chip, st.Tone = ChipAlert, ToneAlert
	case SceneWatch:
		st.Chip, st.Tone = ChipWatch, ToneWatch
	case SceneClear:
		st.Chip, st.Tone = ChipClear, ToneDim
	default:
		st.Chip, st.Tone = ChipEmpty, ToneQuiet
	}

	st.Lines = append(st.Lines, f.mustLines(sc)...)
	st.Lines = append(st.Lines, f.riskLines(sc, mem)...)
	st.Lines = append(st.Lines, f.stateLines(sc)...)
	st.Action = f.action(sc)
	return st
}

// mustLines is "what you must do" — the first part, always. When there is
// nothing to do it says that in one sentence rather than being omitted: a
// missing first part would make the reader hunt for it.
func (f *facts) mustLines(sc Scene) []Line {
	switch sc {
	case SceneAlert:
		var out []Line
		ask := f.asks[0]
		who := f.names.hinted(ask.AgentID, ask.Description)
		s := fmt.Sprintf("You're the blocker. %s is waiting on %s, and nothing it does next happens without you.",
			who, f.askWhat(ask))
		if n := len(f.asks) - 1; n > 0 {
			s += fmt.Sprintf(" %s behind it.", capitalise(countOf(n, "more ask")))
		}
		out = append(out, Line{Part: PartMust, Tone: ToneAlert, Text: s})

		wait := fmt.Sprintf("%s waiting", since(f.waited))
		if f.askShare > 0 {
			wait += fmt.Sprintf(", which is %d%% of its whole life", f.askShare)
		}
		out = append(out, Line{Part: PartMust, Tone: ToneWatch, Text: wait + ". " + f.patience()})

		// The retry count is the one piece of this the platform cannot express
		// directly (DATA-SOURCES §1 caveat 1: a retried ask arrives under a
		// fresh agent id), so folding says so.
		if ask.DupeCount > 1 {
			out = append(out, infer(PartMust, ToneDim,
				fmt.Sprintf("The same request has arrived %d times under fresh ids and is folded into one entry, so this is one decision rather than %d",
					ask.DupeCount, ask.DupeCount),
				hedgeNoSignal))
		}
		return out

	case SceneWatch:
		return []Line{{Part: PartMust, Tone: ToneBody,
			Text: "Nothing needs you right now, which is worth saying before the rest of this."}}

	case SceneClear:
		return []Line{{Part: PartMust, Tone: ToneBody, Text: "Nothing needs you."}}

	default:
		return []Line{{Part: PartMust, Tone: ToneBody, Text: "Nothing's reported yet."}}
	}
}

// riskLines is "what is at risk", most severe first, and capped: past three
// sentences the reader is reading a list, not a briefing.
const riskCap = 3

func (f *facts) riskLines(sc Scene, mem Memory) []Line {
	var out []Line
	add := func(l Line) {
		if len(out) < riskCap {
			out = append(out, l)
		}
	}

	// Stuck first. This is the one claim in the whole region that most wants to
	// overreach — a silent agent LOOKS failed — and DATA-SOURCES §1 caveat 3 is
	// explicit that no failure signal exists, so it is hedged and the reasoning
	// is shown rather than the conclusion asserted.
	if len(f.stuck) > 0 {
		a := f.stuck[0]
		silent := since(f.now.Sub(a.LastEventAt))
		claim := fmt.Sprintf("%s has been silent %s", f.names.of(a), silent)
		if a.OpenCall == nil {
			claim += " with no call open, which reads as wedged rather than busy"
		} else {
			claim += fmt.Sprintf(" while still holding %s, so it may just be slow", a.OpenCall.Tool)
		}
		if n := len(f.stuck) - 1; n > 0 {
			claim += fmt.Sprintf(", and %s the same way", countOf(n, "other is"))
		}
		add(infer(PartRisk, ToneWatch, claim, hedgeUnproven))
	}

	// Contention next: two agents writing one file is the only thing here that
	// can silently destroy work, and it is a fact, not an inference. Only while
	// a writer is still running, though — "are both editing" about two agents
	// that returned five minutes ago is a false present tense, and the resolved
	// case gets its own sentence below.
	for _, c := range f.w.Contentions {
		if f.allReturned(c.AgentIDs) {
			continue
		}
		names := make([]string, 0, len(c.AgentIDs))
		for i, id := range c.AgentIDs {
			hint := ""
			if i < len(c.AgentNames) {
				hint = c.AgentNames[i]
			}
			names = append(names, f.names.hinted(id, hint))
		}
		add(Line{Part: PartRisk, Tone: ToneWatch, Text: fmt.Sprintf(
			"%s are both editing %s. Later write wins and nothing here is coordinating them.",
			nameList(names, 3), shortPath(c.Path))})
		break // the newest one; the contention region lists the rest
	}

	// Parked agents, but only when something is actually blocking them — a
	// queued agent in a healthy run is just queued and is not at risk.
	if sc == SceneAlert && len(f.queued) > 0 {
		add(Line{Part: PartRisk, Tone: ToneDim, Text: fmt.Sprintf(
			"%s behind that answer: %s.",
			capitalise(countOf(len(f.queued), "agent")+plural(len(f.queued), " is parked", " are parked")),
			nameList(f.slugsOf(f.queued), 3))})
	}

	// A long open call is the opposite of stuck and is worth separating: the
	// agent is demonstrably in a call, so the silence is explained and the only
	// question is whether the thing it is in ever exits.
	if len(f.openLong) > 0 {
		a := f.openLong[0]
		add(Line{Part: PartRisk, Tone: ToneDim, Text: fmt.Sprintf(
			"%s has held %s for %s. That's not stuck — it's just never going to exit on its own either.",
			f.names.of(a), a.OpenCall.Tool, since(f.now.Sub(a.OpenCall.Since)))})
	}

	// Luck, separated from competence (§13.2). Only claimed when every agent
	// that touched the file has returned, because until then there is nothing
	// to be relieved about.
	for _, c := range f.w.Contentions {
		if !f.allReturned(c.AgentIDs) {
			continue
		}
		names := make([]string, 0, len(c.AgentIDs))
		for i, id := range c.AgentIDs {
			hint := ""
			if i < len(c.AgentNames) {
				hint = c.AgentNames[i]
			}
			names = append(names, f.names.hinted(id, hint))
		}
		add(infer(PartRisk, ToneDim, fmt.Sprintf(
			"%s both wrote %s and both edits survived, so it's worth a look before you merge",
			nameList(names, 3), shortPath(c.Path)), hedgeOrdering))
		break
	}

	if len(out) == 0 {
		// Honest absence, not silence: an empty risk part would read as an
		// omission, and "I checked and found nothing" is different information
		// from "I didn't say".
		out = append(out, Line{Part: PartRisk, Tone: ToneDim,
			Text: "Nothing else at risk that the events show."})
	}
	return out
}

func (f *facts) allReturned(ids []string) bool {
	for _, id := range ids {
		a, ok := f.agent(id)
		if !ok || a.Status != state.StatusDone {
			return false
		}
	}
	return len(ids) > 0
}

// stateLines is "the state": the totals, last, where they cannot be mistaken
// for the thing that needs doing.
func (f *facts) stateLines(sc Scene) []Line {
	var out []Line

	var parts []string
	if n := len(f.live); n > 0 {
		parts = append(parts, fmt.Sprintf("%d working", n))
	}
	if n := len(f.asking); n > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", n))
	}
	if n := len(f.stuck); n > 0 {
		parts = append(parts, fmt.Sprintf("%d quiet", n))
	}
	if n := len(f.queued); n > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", n))
	}
	if n := len(f.done); n > 0 {
		parts = append(parts, fmt.Sprintf("%d back", n))
	}
	if n := len(f.mates); n > 0 {
		parts = append(parts, fmt.Sprintf("%s on limited telemetry", countOf(n, "teammate")))
	}
	if len(parts) > 0 {
		out = append(out, Line{Part: PartState, Tone: ToneDim, Text: capitalise(strings.Join(parts, ", ")) + "."})
	}

	// The diff and the cost. Both are sums over agents, and the "not merged
	// yet" half is left unsaid because the platform emits no merge signal.
	var tail []string
	if f.linesAdd > 0 || f.linesDel > 0 {
		s := fmt.Sprintf("+%d −%d", f.linesAdd, f.linesDel)
		if f.files > 0 {
			s += fmt.Sprintf(" across %s", countOf(f.files, "file"))
		}
		tail = append(tail, s)
	}
	if t := tokensText(f.tokens); t != "" {
		tail = append(tail, t)
	}
	if f.hasRun && f.runFor > 0 {
		tail = append(tail, since(f.runFor)+" of run")
	}
	if len(tail) > 0 {
		out = append(out, Line{Part: PartState, Tone: ToneQuiet, Text: strings.Join(tail, " · ") + "."})
	}

	// The suppressed agents are state, and stating them here is the same
	// obligation §1.5 puts on the tree: the pane never implies they do not
	// exist.
	if n := len(f.w.Quiet); n > 0 {
		out = append(out, Line{Part: PartState, Tone: ToneQuiet, Text: fmt.Sprintf(
			"%s announced and never did anything; they're on the suppressed line.", countOf(n, "agent"))})
	}
	if !f.w.Connected {
		out = append(out, Line{Part: PartState, Tone: ToneWatch,
			Text: "The event source is disconnected, so none of the above is necessarily current."})
	}
	if len(out) == 0 {
		out = append(out, Line{Part: PartState, Tone: ToneQuiet, Text: "No agents on the tree."})
	}
	return out
}

// action is the closing line, and it is never absent (§13.2: "always ends by
// naming the action or explicitly saying none is needed"). Every branch either
// names keys or says in words that nothing is needed.
func (f *facts) action(sc Scene) Line {
	switch sc {
	case SceneAlert:
		return Line{Part: PartAction, Tone: ToneAlert, Text: "▸ answer in the left pane — ⏎ jumps focus"}
	case SceneWatch:
		if len(f.stuck) > 0 {
			return Line{Part: PartAction, Tone: ToneWatch,
				Text: "▸ nothing needs you — ⏎ inspects the quiet one, y yanks its command"}
		}
		return Line{Part: PartAction, Tone: ToneWatch,
			Text: "▸ nothing needs you — ⏎ inspects, o opens the contested file"}
	case SceneClear:
		if n := len(f.live) + len(f.queued); n > 0 {
			return Line{Part: PartAction, Tone: ToneDim, Text: fmt.Sprintf(
				"▸ nothing needs you — %s still to come back", countOf(n, "agent"))}
		}
		return Line{Part: PartAction, Tone: ToneDim, Text: "▸ nothing needs you — every agent's back"}
	default:
		return Line{Part: PartAction, Tone: ToneQuiet, Text: "▸ nothing needs you yet"}
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

// shortPath matches §2.7's shortening so a path in a sentence and the same path
// on the contention row are the same string.
func shortPath(p string) string { return strings.TrimPrefix(p, "src/") }
