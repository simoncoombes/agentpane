package narrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The commentary filter — the one editorial decision in this package.
//
// §13.2 asks for "a line per event that deserves a sentence", and notes that a
// line per tool call would be noise. The test I settled on, and the test every
// candidate below has to pass:
//
//	A transition earns a sentence when it changes what the reader would do
//	next, or when it contradicts something the narrator has already said.
//
// That single rule decides every case without a taste argument:
//
//   - A tool call does not qualify. The row above already shows it live, and
//     knowing that an agent ran Grep changes nothing about the reader's next
//     move. Twelve of them a second would also drown the four lines that do.
//   - A permission ask qualifies twice over: it changes the reader's next
//     action from "watch" to "answer".
//   - A stall qualifies. So does the agent later returning, because that
//     CONTRADICTS the stall line — the second half of the rule exists to make
//     self-correction mandatory rather than optional (§13.2 "that one's mine").
//   - A return qualifies: one fewer thing to wait for.
//   - A verify outcome qualifies: it is the only evidence in the whole model
//     that the work is right, and its two outcomes point at different actions.
//   - A file collision qualifies: silent data loss is the one risk here the
//     reader cannot see coming.
//   - A station advance, a token update, a decay, a selection change and a
//     scroll do not. None of them change an action, and none of them contradict
//     anything.
//
// Everything is keyed and emitted at most once per Memory, so the filter's
// output is a function of the world's history and not of how often it is
// sampled: observe() re-derives every candidate on every frame at 10fps and
// still appends each line exactly once.
//
// Entries carry the EVENT's time, not the observation time. That is what lets a
// single Observe over a finished world produce a correctly-ordered run history
// instead of a stack of lines all stamped with the same instant.
func observe(f *facts, mem *Memory) []Entry {
	var cand []Entry
	at := func(t time.Time) time.Duration {
		if t.IsZero() || !t.After(f.runStart) {
			return 0
		}
		return t.Sub(f.runStart)
	}

	// --- the run itself ---
	title := f.w.Main.Title
	if title == "" {
		title = f.w.Main.Activity
	}
	// The keys of the two 0:00 entries are chosen so they sort in reading order
	// ("run" before "run-quiet"): At ties break on Key (sortEntries), which is
	// what keeps a batch of same-instant lines in one fixed order, and the run's
	// own opening should lead it.
	if title != "" {
		cand = append(cand, Entry{Key: "run", At: 0, Tone: ToneQuiet,
			Text: "Run opened: " + oneSentence(title) + "."})
	} else if len(f.w.Agents) > 0 {
		cand = append(cand, Entry{Key: "run", At: 0, Tone: ToneQuiet, Text: "Run opened."})
	}

	// The suppressed agents, stated once. Their count can grow later; the line
	// is not restated, because a commentary entry is a record of what was true
	// when it was written and rewriting history is the one thing a log may not
	// do. The standup carries the live number.
	if n := len(f.w.Quiet); n > 0 {
		cand = append(cand, Entry{Key: "run-quiet", At: 0, Tone: ToneQuiet, Text: fmt.Sprintf(
			"%s were announced and never did anything — no name, no tool call, no tokens. Folded into the suppressed line.",
			countOf(n, "subagent"))})
	}

	// --- spawns, folded by instant ---
	//
	// One line per agent would put a wall of near-identical sentences at the top
	// of every run. Agents announced in the same second are one decision by the
	// orchestrator and read as one sentence.
	spawns := map[int64][]state.Agent{}
	var instants []int64
	for _, a := range f.w.Agents {
		if a.SpawnedAt.IsZero() || a.Teammate {
			continue
		}
		k := a.SpawnedAt.Unix()
		if _, ok := spawns[k]; !ok {
			instants = append(instants, k)
		}
		spawns[k] = append(spawns[k], a)
	}
	for _, k := range instants {
		group := spawns[k]
		key := fmt.Sprintf("spawn:%d", k)
		names := nameList(f.slugsOf(group), 4)
		text := fmt.Sprintf("Fanned out %s — %s.", countOf(len(group), "agent"), names)
		if len(group) == 1 {
			text = fmt.Sprintf("Spawned %s.", names)
		}
		cand = append(cand, Entry{Key: key, At: at(group[0].SpawnedAt), Tone: ToneQuiet, Text: text})
	}

	// --- teammates, once each ---
	for _, a := range f.mates {
		cand = append(cand, Entry{Key: "mate:" + a.ID, At: at(a.SpawnedAt), Tone: ToneQuiet, Text: fmt.Sprintf(
			"%s joined as a teammate. No stations, no tokens, no tool calls — that's all the platform reports for a named agent.",
			f.names.of(a))})
	}

	// --- asks ---
	for _, ask := range f.w.Asks {
		who := f.names.hinted(ask.AgentID, ask.Description)
		cand = append(cand, Entry{Key: "ask:" + ask.Key, At: at(ask.RaisedAt), Tone: ToneAlert,
			Text: fmt.Sprintf("%s stopped and asked for %s. Nothing it does next happens without you.",
				who, f.askWhat(ask))})
		if ask.DupeCount > 1 {
			cand = append(cand, inferEntry("ask:"+ask.Key+":dupes", at(ask.RaisedAt), ToneQuiet, fmt.Sprintf(
				"%s arrived under fresh ids and are folded into that one entry, since you don't need telling %d times",
				countOf(ask.DupeCount-1, "more request"), ask.DupeCount), hedgeNoSignal))
		}
		if ask.Resolving {
			cand = append(cand, inferEntry("ask:"+ask.Key+":resolving", at(f.now), ToneDim,
				"Something answered it; waiting for an event that confirms which way", hedgeNoSignal))
		}
		mem.asking[ask.AgentID] = true
	}
	// An ask the narrator announced that is no longer outstanding: the platform
	// emits no grant and no deny (DATA-SOURCES §1 caveat 1), so the resolution
	// is reported as the disappearance it actually is.
	outstanding := map[string]bool{}
	for _, ask := range f.w.Asks {
		outstanding[ask.AgentID] = true
	}
	for _, a := range f.w.Agents {
		if !mem.asking[a.ID] || outstanding[a.ID] {
			continue
		}
		delete(mem.asking, a.ID)
		cand = append(cand, inferEntry("ask-done:"+a.ID, at(a.LastEventAt), ToneDim, fmt.Sprintf(
			"%s is off the band, so the prompt got answered", f.names.of(a)), hedgeNoSignal))
	}

	// --- stalls, and the self-correction they set up ---
	for _, a := range f.stuck {
		silent := since(f.now.Sub(a.LastEventAt))
		claim := fmt.Sprintf("%s went quiet %s ago", f.names.of(a), silent)
		if a.OpenCall == nil {
			claim += " with no call open, no output and no error, which is harder to diagnose than an outright failure"
		} else {
			claim += fmt.Sprintf(" while still holding %s", a.OpenCall.Tool)
		}
		cand = append(cand, inferEntry("stuck:"+a.ID, at(a.LastEventAt), ToneWatch, claim, hedgeUnproven))
		if _, ok := mem.wroteOff[a.ID]; !ok {
			mem.wroteOff[a.ID] = at(a.LastEventAt)
		}
	}

	// --- returns ---
	for _, a := range f.done {
		// "returned", never "finished" or "succeeded": SubagentStop carries no
		// status, no error and no exit code (DATA-SOURCES §1 caveat 3), so
		// return is the only thing that happened and the only thing said.
		text := f.names.of(a) + " returned"
		switch {
		case a.LinesAdd > 0 || a.LinesDel > 0:
			text += fmt.Sprintf(". +%d −%d", a.LinesAdd, a.LinesDel)
			if n := len(a.EditedFiles); n > 0 {
				text += " across " + countOf(n, "file")
			}
		case len(a.EditedFiles) > 0:
			text += ", having touched " + countOf(len(a.EditedFiles), "file")
		}
		cand = append(cand, Entry{Key: "done:" + a.ID, At: at(a.DoneAt), Tone: ToneDim, Text: text + "."})

		// The contradiction. An agent the narrator called wedged that came back
		// on its own proves the earlier line wrong, and §13.2 requires it to
		// own that rather than let the stall line stand.
		if wroteAt, ok := mem.wroteOff[a.ID]; ok {
			gap := at(a.DoneAt) - wroteAt
			cand = append(cand, Entry{Key: "mine:" + a.ID, At: at(a.DoneAt), Tone: ToneBody, Mine: true,
				Text: fmt.Sprintf("%s woke up on its own and returned, about %s after I'd written it off. That one's mine.",
					f.names.of(a), since(gap))})
			delete(mem.wroteOff, a.ID)
		}
	}

	// --- verification ---
	//
	// Stamped with the agent's last event rather than the check's own instant:
	// state.Agent records the OUTCOME (§13.3 Q6) and not when it landed. The
	// entry is therefore ordered by the closest time the model has, which is
	// honest about position and never claims a timestamp the world does not
	// carry.
	for _, a := range f.w.Agents {
		switch a.Verify {
		case state.VerifyOK:
			cand = append(cand, Entry{Key: "verify-ok:" + a.ID, At: at(a.LastEventAt), Tone: ToneDim,
				Text: f.names.of(a) + "'s check exited zero. That's the only evidence here that any of this works."})
		case state.VerifyFailed:
			cand = append(cand, Entry{Key: "verify-bad:" + a.ID, At: at(a.LastEventAt), Tone: ToneWatch,
				Text: f.names.of(a) + "'s check exited non-zero. Whatever it just did isn't finished."})
		}
	}

	// --- errors: the LAST one per agent, not every one ---
	for _, a := range f.w.Agents {
		if a.LastError == "" {
			continue
		}
		cand = append(cand, Entry{Key: "err:" + a.ID + ":" + keyOf(a.LastError), At: at(a.LastEventAt),
			Tone: ToneWatch, Text: fmt.Sprintf("%s hit %s.", f.names.of(a), oneSentence(a.LastError))})
	}

	// --- collisions ---
	for _, c := range f.w.Contentions {
		names := make([]string, 0, len(c.AgentIDs))
		for i, id := range c.AgentIDs {
			hint := ""
			if i < len(c.AgentNames) {
				hint = c.AgentNames[i]
			}
			names = append(names, f.names.hinted(id, hint))
		}
		cand = append(cand, Entry{Key: "clash:" + c.Path, At: at(c.DetectedAt), Tone: ToneWatch,
			Text: fmt.Sprintf("%s both started writing %s. Nobody's coordinating that.",
				nameList(names, 3), shortPath(c.Path))})
		if f.allReturned(c.AgentIDs) {
			cand = append(cand, inferEntry("clash-ok:"+c.Path, at(f.latestDone(c.AgentIDs)), ToneDim,
				fmt.Sprintf("Both edits to %s survived, so read it before you merge", shortPath(c.Path)),
				hedgeOrdering))
		}
	}

	// --- session-level events, emitted on the TRANSITION only ---
	//
	// A world is disconnected for as long as it is disconnected, so testing the
	// state would append a line per repaint; keying it fixed would swallow the
	// second drop of a run. Both are counted on the edge instead.
	if sess := int8(f.w.Session.State) + 1; mem.sessState != sess {
		was := mem.sessState
		mem.sessState = sess
		if f.w.Session.State == state.SessionCompacting {
			mem.compactions++
			cand = append(cand, Entry{Key: fmt.Sprintf("compact:%d", mem.compactions), At: at(f.now),
				Tone: ToneQuiet, Text: "Context compaction started. Row order is held until it finishes."})
		} else if was == int8(state.SessionCompacting)+1 {
			cand = append(cand, Entry{Key: fmt.Sprintf("compact-done:%d", mem.compactions), At: at(f.now),
				Tone: ToneQuiet, Text: "Compaction finished; rows are free to reorder again."})
		}
	}
	if conn := connCode(f.w.Connected); mem.connState != conn {
		was := mem.connState
		mem.connState = conn
		if !f.w.Connected {
			mem.drops++
			cand = append(cand, Entry{Key: fmt.Sprintf("drop:%d", mem.drops), At: at(f.now), Tone: ToneWatch,
				Text: "The event source dropped. Nothing after this line is current until it reconnects."})
		} else if was == connCode(false) {
			cand = append(cand, Entry{Key: fmt.Sprintf("reconnect:%d", mem.drops), At: at(f.now),
				Tone: ToneDim, Text: "Source reconnected. Anything that happened while it was down never reached this pane."})
		}
	}
	if f.w.DroppedEvents > 0 {
		cand = append(cand, Entry{Key: "dropped", At: at(f.now), Tone: ToneQuiet, Text: fmt.Sprintf(
			"%s couldn't be parsed and were discarded; ? says where the log is.",
			countOf(f.w.DroppedEvents, "event"))})
	}

	// Filter by key, order by event time, append.
	fresh := make([]Entry, 0, len(cand))
	for _, e := range cand {
		if e.Text == "" || mem.said[e.Key] {
			continue
		}
		mem.said[e.Key] = true
		fresh = append(fresh, e)
	}
	sortEntries(fresh)
	mem.Entries = append(mem.Entries, fresh...)
	if mem.capN > 0 {
		if over := len(mem.Entries) - mem.capN; over > 0 {
			mem.Entries = mem.Entries[over:]
		}
	}
	return fresh
}

// connCode maps connectedness onto the tri-state Memory stores (0 is "not yet
// observed", which must not read as a transition on the first frame).
func connCode(connected bool) int8 {
	if connected {
		return 1
	}
	return 2
}

func (f *facts) latestDone(ids []string) time.Time {
	var out time.Time
	for _, id := range ids {
		if a, ok := f.agent(id); ok && a.DoneAt.After(out) {
			out = a.DoneAt
		}
	}
	return out
}

// oneSentence flattens platform text for use inside a sentence: whitespace runs
// collapse, the trailing full stop is dropped (the template adds one), and it is
// capped. §13.1 already flattens on ingest; this is the sentence-level cut.
const sentenceCap = 96

func oneSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ".")
	r := []rune(s)
	if len(r) > sentenceCap {
		s = strings.TrimRight(string(r[:sentenceCap-1]), " ") + "…"
	}
	return s
}

// keyOf makes a stable dedupe token out of arbitrary text so an error's key
// changes when the error does and not when its formatting does.
func keyOf(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(s)), " ")
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40])
	}
	return s
}
