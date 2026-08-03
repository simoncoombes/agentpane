package narrate

import (
	"strings"
	"testing"

	"github.com/simoncoombes/agentpane/internal/state"
)

// The tense of a collision's writers is earned per WRITER, from that writer's own
// status — not from "has it returned".
//
// A writer blocked on a permission prompt is not editing anything, and neither is
// one that has gone silent. Testing only for StatusDone put `audit-users-schema is
// still editing db/schema.ts` two lines under `You're the blocker.
// audit-users-schema is waiting on permission…`, which is the standup
// contradicting itself inside one block. Same family as the returned-writer tense
// (TestContentionLineNamesOnlyLiveWriters); this is the rest of the family.
func TestContentionTenseIsEarnedFromEachWritersStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  state.Status
		banned  []string
		wanted  []string
		alsoAsk bool
	}{{
		name:    "blocked on a prompt",
		status:  state.StatusAsk,
		alsoAsk: true,
		banned: []string{
			"audit-users-schema and write-email-index are both editing",
			"audit-users-schema is still editing",
		},
		wanted: []string{
			"write-email-index is still editing db/schema.ts, which audit-users-schema already wrote",
		},
	}, {
		name:   "gone silent",
		status: state.StatusStuck,
		banned: []string{
			"audit-users-schema and write-email-index are both editing",
			"audit-users-schema is still editing",
		},
		wanted: []string{
			"write-email-index is still editing db/schema.ts, which audit-users-schema already wrote",
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := contentionWorld(0)
			agents := append([]state.Agent(nil), w.Agents...)
			agents[0].Status = tc.status
			w.Agents = agents
			if tc.alsoAsk {
				w.Asks = []state.Ask{{
					Key: "Bash|grep", AgentID: "c1", Description: "audit the users schema",
					Tool: "Bash", Command: "grep -rn createUser src", Kind: "command",
					RaisedAt: at(156), DupeCount: 1,
				}}
			}
			got := joined(Narrate(w, Memory{}, snapNow(), opt()).Standup)
			for _, banned := range tc.banned {
				if strings.Contains(got, banned) {
					t.Errorf("a writer that is not writing is in the present tense (%q):\n%s",
						banned, got)
				}
			}
			for _, want := range tc.wanted {
				if !strings.Contains(got, want) {
					t.Errorf("want %q, got:\n%s", want, got)
				}
			}
			// The warning itself is never lost: two agents writing one file is the
			// only thing here that can silently destroy work.
			if !strings.Contains(got, "Later write wins and nothing here is coordinating them") {
				t.Errorf("the collision warning went missing entirely:\n%s", got)
			}
		})
	}
}

// Every writer stopped without all of them returning: one blocked, one quiet. The
// file is still contested and the later write still wins, so the warning is still
// owed — but there is no live writer to put in the present tense, and the luck
// line only speaks for the all-returned case. Gating the whole sentence on "is
// anything live" would have traded a false tense for silence.
func TestContentionWarnsWhenEveryWriterHasStoppedShortOfReturning(t *testing.T) {
	w := contentionWorld(0)
	agents := append([]state.Agent(nil), w.Agents...)
	agents[0].Status = state.StatusStuck
	agents[1].Status = state.StatusAsk
	w.Agents = agents

	got := joined(Narrate(w, Memory{}, snapNow(), opt()).Standup)
	want := "audit-users-schema and write-email-index both wrote db/schema.ts and nothing is writing it now"
	if !strings.Contains(got, want) {
		t.Errorf("want %q, got:\n%s", want, got)
	}
	if strings.Contains(got, "editing") {
		t.Errorf("a stopped writer is described as editing:\n%s", got)
	}
	if !strings.Contains(got, "Later write wins and nothing here is coordinating them") {
		t.Errorf("the collision warning went missing entirely:\n%s", got)
	}
	// And it is NOT the luck line: nothing has returned, so nothing survived yet.
	if strings.Contains(got, hedgeOrdering) {
		t.Errorf("an unfinished collision claimed the luck line:\n%s", got)
	}
}

// scene() reaches SceneAlert on an asking AGENT with no ask record as well as on
// an ask (allAsksResolving documents the same case), and mustLines indexed asks[0]
// unconditionally — so that world panicked the whole pane rather than rendering a
// frame. The standup says only what the world carries there.
func TestAnAskingAgentWithNoAskRecordStillComposes(t *testing.T) {
	w := contentionWorld(0)
	agents := append([]state.Agent(nil), w.Agents...)
	agents[0].Status = state.StatusAsk
	w.Agents = agents
	w.Asks = nil

	st := Narrate(w, Memory{}, snapNow(), opt()).Standup
	if st.Scene != SceneAlert {
		t.Fatalf("scene = %v, want SceneAlert (the case this guards)", st.Scene)
	}
	got := joined(st)
	if !strings.Contains(got, "audit-users-schema is waiting on an answer") {
		t.Errorf("the standup does not name the blocked agent:\n%s", got)
	}
	if !strings.Contains(got, "never reached this pane") {
		t.Errorf("the standup does not say the request itself is missing:\n%s", got)
	}
	if st.Action.Text == "" {
		t.Errorf("the standup lost its closing action:\n%s", got)
	}
}

// The two-form rule (Entry.Cmd / Entry.Gist), at the narrate boundary: the log
// entry carries the verbatim record AND the paraphrase, both fixed when it is
// appended, and TextOn is the only thing that chooses between them.
func TestAskEntryCarriesBothFormsAndTextOnChooses(t *testing.T) {
	w := blockedWorld()
	res := Narrate(w, Memory{}, snapNow(), opt())
	cmd := "rm -rf node_modules/.cache && pnpm rebuild"

	var e Entry
	for _, x := range res.Memory.Entries {
		if strings.HasPrefix(x.Key, "ask:") && strings.Contains(x.Text, cmd) {
			e = x
		}
	}
	if e.Key == "" {
		t.Fatalf("no ask entry recorded the command:\n%v", res.Memory.Entries)
	}
	if e.Cmd != cmd {
		t.Errorf("Cmd = %q, want the normalised command %q", e.Cmd, cmd)
	}
	if strings.Contains(e.Gist, cmd) {
		t.Errorf("the paraphrase still carries the bytes: %q", e.Gist)
	}
	if !strings.Contains(e.Gist, "permission to run one Bash command") {
		t.Errorf("the paraphrase does not name the kind of ask: %q", e.Gist)
	}

	onScreen := CommandsOnScreen(w)
	if !onScreen[cmd] {
		t.Fatalf("CommandsOnScreen missed the live ask's command: %v", onScreen)
	}
	if got := e.TextOn(onScreen); got != e.Gist {
		t.Errorf("with the band showing the bytes, TextOn returned the record:\n%s", got)
	}
	if got := e.TextOn(nil); got != e.Text {
		t.Errorf("with the band clear, TextOn did not return the record:\n%s", got)
	}
	// The record survives in the Memory either way: nothing is rewritten.
	if !strings.Contains(e.Text, "`"+cmd+"`") {
		t.Errorf("the log's record lost the approved bytes: %q", e.Text)
	}

	// An entry that quotes nothing has no second form, so TextOn is the identity.
	for _, x := range res.Memory.Entries {
		if x.Cmd == "" && x.TextOn(onScreen) != x.Text {
			t.Errorf("TextOn rewrote an entry with no command: %q", x.Text)
		}
	}

	// A command longer than cmdCap is already abbreviated in the record, so the
	// frame cannot be stating it twice and no second form is needed.
	long := blockedWorld()
	long.Asks[0].Command = strings.Repeat("echo verylongcommand && ", 5) + "true"
	long.Asks[0].Key = "Bash|long"
	for _, x := range Narrate(long, Memory{}, snapNow(), opt()).Memory.Entries {
		if strings.HasPrefix(x.Key, "ask:Bash|long") && x.Cmd != "" {
			t.Errorf("a capped command was given a second form: %q", x.Text)
		}
	}
}
