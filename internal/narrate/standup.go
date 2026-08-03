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
		if len(f.asks) == 0 {
			// scene() reaches SceneAlert on an asking AGENT with no ask record too
			// (allAsksResolving names the same case), and indexing asks[0] on that
			// world panicked the whole pane. The agent's status is all the world
			// carries there, so it is all that is claimed.
			who := f.names.of(f.asking[0])
			out = append(out, Line{Part: PartMust, Tone: ToneAlert, Text: fmt.Sprintf(
				"You're the blocker. %s is waiting on an answer, and the request itself never reached this pane.",
				who)})
			if n := len(f.asking) - 1; n > 0 {
				out = append(out, Line{Part: PartMust, Tone: ToneAlert,
					Text: fmt.Sprintf("%s behind it.", capitalise(countOf(n, "more agent")))})
			}
			return out
		}
		ask := f.asks[0]
		who := f.names.hinted(ask.AgentID, ask.Description)

		// The ask is named by KIND, never by command: §3.7 gives the exact string
		// to the band's own row and prose that repeated it stated the ask twice in
		// one region. See askGist.
		if ask.Resolving {
			// §3.7.8. Ask.Resolving means a grant, or an activity event after the
			// prompt, was observed — not that the outcome is known. Denying emits
			// nothing at all (DATA-SOURCES §1 caveat 1), so the entry clears late,
			// and telling this reader they are the blocker would be asking them to
			// answer a prompt they have already answered. It is a reading of two
			// events rather than a report of one, so it is marked as one.
			out = append(out, infer(PartMust, ToneWatch, fmt.Sprintf(
				"%s's answer is in and the outcome isn't confirmed yet: a denial emits nothing at all, so this clears late rather than the moment you answer",
				who), hedgeNoSignal))
			if n := len(f.asks) - 1; n > 0 {
				out = append(out, Line{Part: PartMust, Tone: ToneAlert,
					Text: fmt.Sprintf("%s behind it.", capitalise(countOf(n, "more ask")))})
			}
		} else {
			s := fmt.Sprintf("You're the blocker. %s is waiting on %s, and nothing it does next happens without you.",
				who, f.askGist(ask))
			if n := len(f.asks) - 1; n > 0 {
				s += fmt.Sprintf(" %s behind it.", capitalise(countOf(n, "more ask")))
			}
			out = append(out, Line{Part: PartMust, Tone: ToneAlert, Text: s})
		}

		wait := fmt.Sprintf("%s waiting", since(f.waited))
		if f.askShare > 0 {
			wait += fmt.Sprintf(", which is %d%% of its whole life", f.askShare)
		}
		if ask.Resolving {
			// The ladder is pressure to press a key. There is no key left to press
			// here, so the wait is reported and nothing is argued from it.
			out = append(out, Line{Part: PartMust, Tone: ToneWatch, Text: wait + "."})
		} else {
			out = append(out, Line{Part: PartMust, Tone: ToneWatch, Text: wait + ". " + f.patience()})
		}

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
	// can silently destroy work, and it is a fact, not an inference. The tense
	// has to be earned per WRITER, though, not per collision. Gating the whole
	// sentence on "have they all returned" put a live present tense on writers
	// that had already come back — "x and y are both editing db/schema.ts" two
	// lines above "1 quiet, 1 back", which is the standup contradicting itself
	// inside one block. So the still-live writers are named in the present and
	// the returned ones in the past, and a collision with no live writer left is
	// skipped here entirely (the luck line below owns the resolved case).
	for _, c := range f.w.Contentions {
		live, stopped := f.splitWriters(c.AgentIDs, c.AgentNames)
		var text string
		switch {
		case len(live) == 0 && len(stopped) > 0 && !f.allReturned(c.AgentIDs):
			// Every writer has stopped without all of them returning: one is blocked
			// on a prompt, or gone quiet. The warning is still owed — the file is
			// still contested and the later write still wins — but there is no live
			// writer to put in the present tense, and the luck line below only
			// speaks for the all-returned case, so this one would have gone unsaid.
			verb := "both wrote"
			if len(stopped) == 1 {
				verb = "wrote"
			} else if len(stopped) > 2 {
				verb = "all wrote"
			}
			text = fmt.Sprintf("%s %s %s and nothing is writing it now. Later write wins and nothing here is coordinating them.",
				nameList(stopped, 3), verb, shortPath(c.Path))
		case len(live) == 0:
			continue
		case len(stopped) == 0:
			verb := "are both editing"
			if len(live) == 1 {
				verb = "is editing"
			} else if len(live) > 2 {
				verb = "are all editing"
			}
			text = fmt.Sprintf("%s %s %s. Later write wins and nothing here is coordinating them.",
				nameList(live, 3), verb, shortPath(c.Path))
		default:
			text = fmt.Sprintf("%s %s still editing %s, which %s already wrote. Later write wins and nothing here is coordinating them.",
				nameList(live, 3), plural(len(live), "is", "are"), shortPath(c.Path),
				nameList(stopped, 3))
		}
		add(Line{Part: PartRisk, Tone: ToneWatch, Text: text})
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
	//
	// That question is NOT answered by the events. openLong is any open call past
	// openCallLong, so a slow-but-finite build lands here alongside a watcher, and
	// the old flat "it's just never going to exit on its own either" asserted the
	// watcher's ending for both. What the events support is the duration; the rest
	// is an inference, hedged, and it only names the watcher reading when the
	// command itself is watch-shaped — read with state.IsWatchCommand, the same
	// test the stall threshold uses, so the prose and the machine cannot disagree
	// about what a watcher looks like.
	if len(f.openLong) > 0 {
		a := f.openLong[0]
		held := fmt.Sprintf("%s has held %s for %s, so it's inside a call rather than silent",
			f.names.of(a), a.OpenCall.Tool, since(f.now.Sub(a.OpenCall.Since)))
		if state.IsWatchCommand(a.OpenCall.TargetRaw()) {
			add(infer(PartRisk, ToneDim, held+
				", and the command is watch-shaped, so it is either mid-work or a watcher that will never exit on its own",
				hedgeCantTell))
		} else {
			add(infer(PartRisk, ToneDim, held+
				", and it is either still working or wedged inside that call", hedgeCantTell))
		}
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

// splitWriters names a collision's participants in two groups: the ones that
// could still be writing and the ones that have stopped, both in the contention's
// own id order so a sentence lists them the way the contention row does.
//
// "Stopped" is not just StatusDone. An agent blocked on a permission prompt, or
// one that has gone silent, is not editing anything — testing only for Done put
// `audit-users-schema is still editing db/schema.ts` two lines under `You're the
// blocker. audit-users-schema is waiting on permission…`, which is the standup
// contradicting itself inside one block. Same family as the returned-writer tense:
// the tense has to be earned per WRITER, from that writer's own status.
//
// A writer the World no longer carries counts as LIVE, matching allReturned's
// conservatism: the events do not say it stopped, so the prose may not either.
// The alternative — treating an absent record as settled — would drop the one
// warning about a file that two agents are writing.
func (f *facts) splitWriters(ids, hints []string) (live, stopped []string) {
	for i, id := range ids {
		hint := ""
		if i < len(hints) {
			hint = hints[i]
		}
		name := f.names.hinted(id, hint)
		if f.writerStopped(id) {
			stopped = append(stopped, name)
			continue
		}
		live = append(live, name)
	}
	return live, stopped
}

// writerStopped reports that this participant is demonstrably not writing right
// now: it has returned, it is blocked on a prompt, or it has gone silent. It is
// deliberately NOT writerReturned — that predicate answers "is the collision
// settled", which gates the scene and the luck line, and a blocked writer settles
// nothing.
func (f *facts) writerStopped(id string) bool {
	a, ok := f.agent(id)
	if !ok {
		return false
	}
	switch a.Status {
	case state.StatusDone, state.StatusAsk, state.StatusStuck:
		return true
	}
	return false
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
		// Every outstanding ask already has an answer in: there is nothing to
		// press, and §3.7.8's late clear is what the line reports instead. One ask
		// still unanswered and the reader is still the blocker for that one, so the
		// keys come back.
		if f.allAsksResolving() {
			return Line{Part: PartAction, Tone: ToneWatch, Text: "▸ " + ActionResolving}
		}
		return Line{Part: PartAction, Tone: ToneAlert, Text: "▸ answer in the left pane — ⏎ jumps focus"}
	case SceneWatch:
		// One branch per thing that can PUT the world in SceneWatch (scene():
		// stuck, a live contention, a long open call), because the action has to
		// name something the world actually contains. The old fall-through offered
		// "o opens the contested file" whenever nothing was stuck — including when
		// the scene was reached by a long open call alone and no file was contested
		// at all, which pointed the reader at a file that did not exist.
		switch {
		case len(f.stuck) > 0:
			return Line{Part: PartAction, Tone: ToneWatch,
				Text: "▸ nothing needs you — ⏎ inspects the quiet one, y yanks its command"}
		case f.liveContention():
			return Line{Part: PartAction, Tone: ToneWatch,
				Text: "▸ nothing needs you — ⏎ inspects a writer, o opens the file it last touched"}
		case len(f.openLong) > 0:
			return Line{Part: PartAction, Tone: ToneWatch,
				Text: "▸ nothing needs you — ⏎ inspects the open call, y yanks its command"}
		default:
			return Line{Part: PartAction, Tone: ToneWatch,
				Text: "▸ nothing needs you — ⏎ inspects"}
		}
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
