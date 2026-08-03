package narrate

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

// --- the commentary's timestamp column ---

// A stall is stamped from the agent's LAST EVENT, which can be minutes older than
// a return the narrator has already logged. Appending the batch put the older
// entry last, and the renderer prints slice order, so the log read 0:00, 0:00,
// 4:00, 2:00 — a timestamp column running backwards, which reads as a rendering
// fault rather than as history.
func TestCommentaryStaysOrderedWhenDetectionIsOutOfOrder(t *testing.T) {
	// Frame one: the build agent has already come back at 4:00.
	first := Narrate(lateStallWorld(false), Memory{}, at(245), opt())
	if countContaining(first.New, "returned") != 1 {
		t.Fatalf("frame one did not log the return: %+v", first.New)
	}

	// Frame two, ninety seconds later: the OTHER agent is declared quiet, and its
	// last event was at 2:00 — before the return that is already in the log.
	second := Narrate(lateStallWorld(true), first.Memory, at(330), opt())
	var stall Entry
	for _, e := range second.New {
		if strings.Contains(e.Text, "went quiet") {
			stall = e
		}
	}
	if stall.Key == "" {
		t.Fatalf("frame two did not write the quiet agent off: %+v", second.New)
	}
	if stall.At != 120*1e9 {
		t.Fatalf("stall stamped at %s, want 2:00 (the agent's last event)", clock(stall.At))
	}

	entries := second.Memory.Entries
	for i := 1; i < len(entries); i++ {
		if entries[i].At < entries[i-1].At {
			t.Errorf("entry %d (%s %q) precedes %d (%s %q)",
				i, clock(entries[i].At), entries[i].Text,
				i-1, clock(entries[i-1].At), entries[i-1].Text)
		}
	}
	// And specifically: the 2:00 stall sits BEFORE the 4:00 return it was
	// detected after.
	si, ri := -1, -1
	for i, e := range entries {
		switch {
		case strings.Contains(e.Text, "went quiet"):
			si = i
		case strings.Contains(e.Text, "returned"):
			ri = i
		}
	}
	if si < 0 || ri < 0 {
		t.Fatalf("log is missing one of the two entries: %+v", entries)
	}
	if si > ri {
		t.Errorf("the 2:00 stall was appended after the 4:00 return (indices %d, %d)", si, ri)
	}
}

// mergeEntries is the invariant on its own: two ordered runs in, one ordered run
// out, and an entry already in the log wins a tie so re-narration is stable.
func TestMergeEntriesKeepsTheLogOrdered(t *testing.T) {
	log := []Entry{{Key: "a", At: 0}, {Key: "b", At: 240 * 1e9}}
	fresh := []Entry{{Key: "c", At: 120 * 1e9}, {Key: "d", At: 240 * 1e9}, {Key: "e", At: 400 * 1e9}}
	got := mergeEntries(log, fresh)
	var keys []string
	for i, e := range got {
		keys = append(keys, e.Key)
		if i > 0 && e.At < got[i-1].At {
			t.Fatalf("merge produced %v, which is not ordered", keys)
		}
	}
	if strings.Join(keys, "") != "acbde" {
		t.Errorf("merge order = %q, want %q (the existing 240s entry keeps its place)",
			strings.Join(keys, ""), "acbde")
	}
	if len(mergeEntries(nil, nil)) != 0 {
		t.Error("merging nothing into nothing produced entries")
	}
}

// lateStallWorld: one agent back at 4:00, one whose last event was at 2:00 and
// which is declared quiet only later.
func lateStallWorld(quiet bool) state.World {
	back := agent("d1", "run the build", state.StatusDone, 4, 240)
	back.DoneAt = at(240)
	back.Tokens = 1000
	back.Station = 3

	late := agent("s1", "probe the cache", state.StatusRun, 4, 120)
	late.Tokens = 500
	if quiet {
		late.Status = state.StatusStuck
	}
	return state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Title: "one thing", LastEventAt: at(240)},
		Agents:    []state.Agent{back, late},
		Run:       &state.Run{ID: "r", StartedAt: base()},
		Connected: true,
	}
}

// --- the contention line's tense ---

// The present tense has to be earned per writer. Gating the whole sentence on
// "have they all returned" put "are both editing" on an agent that had already
// come back, two lines above the state line that said so.
func TestContentionLineNamesOnlyLiveWriters(t *testing.T) {
	t.Run("two live", func(t *testing.T) {
		got := joined(Narrate(contentionWorld(0), Memory{}, snapNow(), opt()).Standup)
		want := "audit-users-schema and write-email-index are both editing db/schema.ts"
		if !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
	})

	t.Run("one live", func(t *testing.T) {
		st := Narrate(contentionWorld(1), Memory{}, snapNow(), opt()).Standup
		got := joined(st)
		if strings.Contains(got, "are both editing") {
			t.Errorf("present tense claimed for a writer that returned:\n%s", got)
		}
		want := "audit-users-schema is still editing db/schema.ts, which write-email-index already wrote"
		if !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
		// The contradiction the finding is about: the state line counts the
		// returned writer, so the risk line above it may not have it editing.
		if !strings.Contains(got, "1 back") {
			t.Fatalf("the state line does not report the returned writer:\n%s", got)
		}
	})

	t.Run("none live", func(t *testing.T) {
		st := Narrate(contentionWorld(2), Memory{}, snapNow(), opt()).Standup
		got := joined(st)
		if strings.Contains(got, "editing") {
			t.Errorf("a finished collision is still described as editing:\n%s", got)
		}
		if !strings.Contains(got, hedgeOrdering) {
			t.Errorf("the resolved collision lost its luck-vs-coordination line:\n%s", got)
		}
		if st.Chip != ChipClear {
			t.Errorf("chip = %q, want ALL CLEAR once every writer is back", st.Chip)
		}
	})
}

// contentionWorld: two agents writing one file, with `returned` of them settled.
// The writers settle newest-first, so the live one is always the first named.
func contentionWorld(returned int) state.World {
	w1 := agent("c1", "audit the users schema", state.StatusRun, 4, 200)
	w1.EditedFiles = []string{"db/schema.ts"}
	w1.Tokens = 1000
	w2 := agent("c2", "write the email index", state.StatusRun, 4, 201)
	w2.EditedFiles = []string{"db/schema.ts"}
	w2.Tokens = 2000

	ws := []state.Agent{w1, w2}
	for i := 0; i < returned && i < len(ws); i++ {
		j := len(ws) - 1 - i
		ws[j].Status = state.StatusDone
		ws[j].DoneAt = at(202)
		ws[j].Station = 3
	}
	return state.World{
		Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:    state.Main{Title: "one thing", LastEventAt: at(205)},
		Agents:  ws,
		Contentions: []state.Contention{{
			Path: "db/schema.ts", AgentIDs: []string{"c1", "c2"},
			AgentNames: []string{"audit the users schema", "write the email index"},
			DetectedAt: at(182),
		}},
		Run:       &state.Run{ID: "r", StartedAt: base()},
		Connected: true,
	}
}

// --- the open-call line's prediction ---

// openLong is any open call past three minutes, so a slow-but-finite build lands
// in it. The events support the duration and nothing more; "it's just never going
// to exit on its own either" is a prediction about the command's ending, and a
// build has one.
func TestOpenCallLineDoesNotPredictTheEnding(t *testing.T) {
	st := Narrate(openCallWorld("make build"), Memory{}, snapNow(), opt()).Standup
	line, ok := riskLineContaining(st, "has held")
	if !ok {
		t.Fatalf("no open-call line:\n%s", joined(st))
	}
	if !strings.Contains(line.Text, "has held Bash for 3m28s") {
		t.Errorf("the duration is not reported from the world: %q", line.Text)
	}
	if strings.Contains(line.Text, "never") {
		t.Errorf("a finite build is told it never exits: %q", line.Text)
	}
	if !line.Inferred || !strings.HasSuffix(line.Text, hedgeCantTell+".") {
		t.Errorf("the open-call reading is not marked as an inference: %q (inferred=%v)",
			line.Text, line.Inferred)
	}
}

// A watcher genuinely does not exit, and the command says so — but it is still
// read off the command text rather than observed, so it is stated as an
// inference and hedged like one.
func TestOpenCallLineReadsAWatcherAsAnInference(t *testing.T) {
	st := Narrate(openCallWorld("pnpm test --watch"), Memory{}, snapNow(), opt()).Standup
	line, ok := riskLineContaining(st, "has held")
	if !ok {
		t.Fatalf("no open-call line:\n%s", joined(st))
	}
	for _, want := range []string{"watch-shaped", "never exit on its own"} {
		if !strings.Contains(line.Text, want) {
			t.Errorf("line %q is missing %q", line.Text, want)
		}
	}
	if !line.Inferred || !strings.HasSuffix(line.Text, hedgeCantTell+".") {
		t.Errorf("the watcher reading is not hedged: %q (inferred=%v)", line.Text, line.Inferred)
	}
	// And it agrees with the state machine about what a watcher is, rather than
	// keeping a second definition in the prose.
	if !state.IsWatchCommand("pnpm test --watch") || state.IsWatchCommand("make build") {
		t.Error("the prose and the stall threshold disagree about watch-shaped commands")
	}
}

// --- the closing action ---

// The action must name something the world contains. SceneWatch is reachable by a
// long open call alone, and the old fall-through offered "o opens the contested
// file" for a world with no contention at all.
func TestWatchActionMatchesWhatPutTheWorldInWatch(t *testing.T) {
	t.Run("long open call, no contention and no stall", func(t *testing.T) {
		st := Narrate(openCallWorld("make build"), Memory{}, snapNow(), opt()).Standup
		if st.Chip != ChipWatch {
			t.Fatalf("chip = %q, want WORTH WATCHING", st.Chip)
		}
		if strings.Contains(st.Action.Text, "file") {
			t.Errorf("the action names a file in a world with no contention: %q", st.Action.Text)
		}
		if !strings.Contains(st.Action.Text, "open call") {
			t.Errorf("the action does not name the thing being watched: %q", st.Action.Text)
		}
	})

	t.Run("live contention", func(t *testing.T) {
		st := Narrate(contentionWorld(0), Memory{}, snapNow(), opt()).Standup
		if st.Chip != ChipWatch {
			t.Fatalf("chip = %q, want WORTH WATCHING", st.Chip)
		}
		// `o` opens the SELECTED agent's last-touched file, so that is what the
		// line may promise — a writer's last-touched file is the contested one.
		if !strings.Contains(st.Action.Text, "o opens the file it last touched") {
			t.Errorf("the action does not describe what o actually does: %q", st.Action.Text)
		}
	})

	t.Run("stall", func(t *testing.T) {
		st := Narrate(riskyWorld(), Memory{}, snapNow(), opt()).Standup
		if !strings.Contains(st.Action.Text, "quiet one") {
			t.Errorf("a stalled world's action does not point at it: %q", st.Action.Text)
		}
	})
}

// openCallWorld: one agent, one open Bash call held since the run started.
func openCallWorld(cmd string) state.World {
	a := agent("o1", "build the bundle", state.StatusRun, 4, 200)
	a.Tokens = 5000
	a.OpenCall = &state.OpenCall{Tool: "Bash", Target: cmd, Since: base()}
	return state.World{
		Session:   state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:      state.Main{Title: "one thing", LastEventAt: at(205)},
		Agents:    []state.Agent{a},
		Run:       &state.Run{ID: "r", StartedAt: base()},
		Connected: true,
	}
}

func riskLineContaining(st Standup, s string) (Line, bool) {
	for _, l := range st.Lines {
		if l.Part == PartRisk && strings.Contains(l.Text, s) {
			return l, true
		}
	}
	return Line{}, false
}

// --- the name freeze ---

// Named freezes a slug for the rest of the run (§13.1(a)), so an id in it is a
// claim that the reader saw that name. A substring search made the claim for two
// agents whose names never appeared: one whose slug is a fragment of another's,
// and one whose slug happens to be a token inside the command an ask quotes.
func TestNamedDoesNotFreezeANameTheTextNeverShowed(t *testing.T) {
	r := Narrate(namedBoundaryWorld(), Memory{}, snapNow(), opt())
	body := joined(r.Standup)
	for _, e := range r.New {
		body += "\n" + e.Text
	}
	// Preconditions: the asker's slug contains the second agent's slug, and the
	// quoted command contains the third's. Both are in the frame's text as raw
	// characters, and neither is a reference to those agents.
	if !strings.Contains(body, "fix-thing") || !strings.Contains(body, "`pnpm lint-and-format --fix`") {
		t.Fatalf("fixture no longer sets up the two boundary cases:\n%s", body)
	}

	named := map[string]bool{}
	for _, id := range r.Named {
		named[id] = true
	}
	if !named["x1"] {
		t.Errorf("the asker, named in the standup, is not frozen: %v", r.Named)
	}
	if named["x2"] {
		t.Errorf("froze x2 (slug \"thing\") because it is a fragment of \"fix-thing\": %v", r.Named)
	}
	if named["x3"] {
		t.Errorf("froze x3 (slug \"lint-and-format\") because it is a token in the quoted command: %v",
			r.Named)
	}
}

// namedBoundaryWorld: an asker whose slug contains another agent's slug, and an
// ask whose quoted command contains a third agent's slug. Only the asker is
// referred to by name.
func namedBoundaryWorld() state.World {
	asker := agent("x1", "fix the thing", state.StatusAsk, 4, 156)
	asker.Tokens = 1000

	// No spawn record, so neither of these earns a commentary line of its own;
	// the standup counts them without naming them.
	frag := state.Agent{ID: "x2", Name: "thing", NameExact: true, Status: state.StatusRun,
		LastEventAt: at(200), Tokens: 10}
	quoted := state.Agent{ID: "x3", Name: "lint and format", NameExact: true, Status: state.StatusRun,
		LastEventAt: at(201), Tokens: 20}

	return state.World{
		Session: state.Session{ID: "s", ShortID: "s", State: state.SessionActive},
		Main:    state.Main{Title: "one thing", LastEventAt: at(205)},
		Agents:  []state.Agent{asker, frag, quoted},
		Asks: []state.Ask{{
			Key: "k", AgentID: "x1", Description: "fix the thing", Tool: "Bash",
			Command: "pnpm lint-and-format --fix", Kind: "command",
			RaisedAt: at(156), DupeCount: 1,
		}},
		Run:       &state.Run{ID: "r", StartedAt: base()},
		Connected: true,
	}
}

// mentions is the word-boundary rule the freeze rests on, including the case the
// slug table's own truncation creates: "typecheck-works" is not a mention inside
// "typecheck-works…", which is a DIFFERENT agent's name.
func TestMentionsRequiresAWholeToken(t *testing.T) {
	cases := []struct {
		text, slug string
		want       bool
	}{
		{"write-email-index returned.", "write-email-index", true},
		{"write-email-index returned.", "email", false},
		{"write-email-index returned.", "index", false},
		{"fix-thing stopped and asked.", "thing", false},
		{"audit-users-schema's check exited zero.", "audit-users-schema", true},
		{"Spawned lint-and-format.", "lint-and-format", true},
		{"typecheck-works… went quiet 40s ago.", "typecheck-works", false},
		{"typecheck-works… went quiet 40s ago.", "typecheck-works…", true},
		{"Fanned out 2 agents — a and b.", "a", true},
		{"", "a", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := mentions(c.text, c.slug); got != c.want {
			t.Errorf("mentions(%q, %q) = %v, want %v", c.text, c.slug, got, c.want)
		}
	}
}

// stripQuoted takes the verbatim command out of the search space, because a token
// inside a shell command is a shell token. An unbalanced backtick can only come
// from a command that contained one, and must not swallow the sentence.
func TestStripQuotedRemovesOnlyTheCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"asked for permission to run `pnpm lint-and-format --fix`. Nothing moves.",
			"asked for permission to run  . Nothing moves."},
		{"no backticks here", "no backticks here"},
		{"trailing `unclosed command", "trailing  unclosed command"},
	}
	for _, c := range cases {
		if got := stripQuoted(c.in); got != c.want {
			t.Errorf("stripQuoted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
