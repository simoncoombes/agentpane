package fake

import (
	"sort"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// SnapshotSeconds is the timeline offset, in seconds, at which the scripted
// state matches the §2.10 table exactly (blocked 52s, silent 2m50s, in call
// 3m12s, returned 46s ago). Seeking here or beyond lands on the canonical
// screenshot state.
const SnapshotSeconds = 300

// epoch anchors every scripted timestamp. Event content never touches the
// wall clock (determinism requirement, SPEC §1.4).
var epoch = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// Epoch returns the fixed timeline origin.
func Epoch() time.Time { return epoch }

const (
	sessionID = "3f9c1a2b-4c5d-4e7f-8901-23456789abcd"

	// 17-hex agent ids, the shape Claude Code 2.1.220 emits
	// (DATA-SOURCES §3.3).
	idFix      = "ada0620f224b32d01"
	idType     = "ada0620f224b32d02"
	idTests    = "ada0620f224b32d03"
	idAudit    = "ada0620f224b32d04"
	idIndex    = "ada0620f224b32d05"
	idLint     = "ada0620f224b32d06"
	idDocs     = "ada0620f224b32d07"
	idReviewer = "ada0620f224b32d08"
	// Ghost ids: the background retrier re-raises the ask under fresh agent
	// ids (DATA-SOURCES §3.3 PermissionRequest). They must have no other
	// events so the state machine can fold them (§3.7 item 7).
	idGhost1 = "ada0620f224b32e01"
	idGhost2 = "ada0620f224b32e02"

	// Full spawn descriptions, the §2.10 / §3.16 long forms. The UI slugs
	// them; they must produce the §2.10 row names.
	descFix   = "Fix the TS2345 fallout in api handlers"
	descType  = "Typecheck the workspace packages"
	descTests = "Run the auth test suite in watch mode"
	descAudit = "Audit the users schema for missing indexes"
	descIndex = "Write the email index migration"
	descLint  = "Lint and format the changed files"
	descDocs  = "Document the new email constraint"

	askCommand   = "rm -rf node_modules/.cache && pnpm rebuild"
	typecheckCmd = "pnpm typecheck"
	watchCmd     = "pnpm test --watch"
	watchOutput  = "/private/tmp/claude-501/demo/3f9c/tasks/" + idTests + ".output"
)

type entry struct {
	ms int
	ev event.Event
}

type builder struct{ entries []entry }

func (b *builder) at(ms int, ev event.Event) {
	if ev.SessionID == "" {
		ev.SessionID = sessionID
	}
	ev.At = epoch.Add(time.Duration(ms) * time.Millisecond)
	b.entries = append(b.entries, entry{ms, ev})
}

func (b *builder) events() []event.Event {
	sort.SliceStable(b.entries, func(i, j int) bool { return b.entries[i].ms < b.entries[j].ms })
	out := make([]event.Event, len(b.entries))
	for i, e := range b.entries {
		out[i] = e.ev
	}
	return out
}

type demoAgent struct {
	id, name, typ string
	b             *builder
}

func (a demoAgent) ev(ms int, e event.Event) {
	e.AgentID = a.id
	if e.AgentType == "" {
		e.AgentType = a.typ
	}
	a.b.at(ms, e)
}

// spawn is the only event that carries the full description; later platform
// events carry ids only (DATA-SOURCES §3.3).
func (a demoAgent) spawn(ms int, detail string) {
	a.b.at(ms, event.Event{
		AgentID: a.id, AgentName: a.name, AgentType: a.typ,
		Kind: event.AgentSpawned, Detail: detail,
	})
}

func (a demoAgent) started(ms int) { a.ev(ms, event.Event{Kind: event.AgentStarted}) }

func (a demoAgent) toolStart(ms int, tool, target, detail string) {
	a.ev(ms, event.Event{Kind: event.ToolStart, Tool: tool, Target: target, Detail: detail})
}

func (a demoAgent) toolEnd(ms int, tool, target, detail string, exit *int, errText string) {
	a.ev(ms, event.Event{Kind: event.ToolEnd, Tool: tool, Target: target, Detail: detail, ExitCode: exit, Err: errText})
}

func (a demoAgent) read(ms int, path string) {
	a.toolStart(ms, "Read", path, "")
	a.ev(ms+900, event.Event{Kind: event.FileRead, Tool: "Read", Target: path})
	a.toolEnd(ms+1000, "Read", path, "", nil, "")
}

func (a demoAgent) edit(ms, durMs int, tool, path string, add, del int) {
	// Hooks order (DATA-SOURCES §3.3 PostToolUse): ToolEnd first, then its
	// FileEdited twin, which the machine merges into the same call record so
	// one edit never shows as two calls.
	a.toolStart(ms, tool, path, "")
	a.toolEnd(ms+durMs, tool, path, "", nil, "")
	a.ev(ms+durMs, event.Event{Kind: event.FileEdited, Tool: tool, Target: path, LinesAdd: add, LinesDel: del})
}

func (a demoAgent) tokens(ms, cum int) {
	a.ev(ms, event.Event{Kind: event.TokensUpdated, Tokens: cum, TokensCum: true})
}

func code(n int) *int { return &n }

func askEvent(agentID string) event.Event {
	return event.Event{
		AgentID:   agentID,
		AgentType: "general-purpose",
		Kind:      event.PermissionRequested,
		Tool:      "Bash",
		Target:    askCommand,
		Detail:    "asked to clear the TS cache",
		Ask: &event.AskPayload{
			Kind:    "command",
			Command: askCommand,
			Options: []string{"once", "always", "no"},
		},
	}
}

// script builds the full §2.10 timeline, fresh on every call so no consumer
// can mutate shared state (pointer fields are reallocated per replay).
//
// The timeline drives the §2.10 end states purely through causal events —
// never a derived Kind — and no run-status agent is ever left quiet past the
// 45s stall threshold except typecheck-workspace, whose silence from t=130s
// is the scripted stuck case.
func script() []event.Event {
	b := &builder{}

	main := demoAgent{id: event.MainAgentID, b: b}
	fix := demoAgent{id: idFix, name: descFix, typ: "general-purpose", b: b}
	typ := demoAgent{id: idType, name: descType, typ: "general-purpose", b: b}
	tests := demoAgent{id: idTests, name: descTests, typ: "general-purpose", b: b}
	audit := demoAgent{id: idAudit, name: descAudit, typ: "general-purpose", b: b}
	index := demoAgent{id: idIndex, name: descIndex, typ: "general-purpose", b: b}
	lint := demoAgent{id: idLint, name: descLint, typ: "general-purpose", b: b}
	docs := demoAgent{id: idDocs, name: descDocs, typ: "general-purpose", b: b}
	reviewer := demoAgent{id: idReviewer, name: "reviewer", typ: "teammate", b: b}

	// Session bring-up; the prompt at t=0 starts the run (§2.8).
	b.at(0, event.Event{Kind: event.SourceConnected, Detail: "demo timeline attached"})
	b.at(0, event.Event{AgentID: event.MainAgentID, Kind: event.SessionStart, Detail: "startup"})
	b.at(0, event.Event{AgentID: event.MainAgentID, Kind: event.UserPromptSubmitted, Detail: "refactor auth"})

	// main: todo list plus steady token growth. Detail on TodoWrite events
	// carries the current in-progress item, which is what the root row
	// shows instead of pips (§2.2). Final item: "write the migration", 6s
	// before the snapshot.
	main.toolStart(2000, "TodoWrite", "", "audit users schema")
	main.toolEnd(3000, "TodoWrite", "", "audit users schema", nil, "")
	for _, t := range []struct{ ms, k int }{
		{4000, 6}, {30000, 12}, {60000, 18}, {90000, 26}, {120000, 35},
		{150000, 48}, {180000, 62}, {210000, 78}, {240000, 92},
		{270000, 104}, {292000, 112},
	} {
		main.tokens(t.ms, t.k*1000)
	}
	main.toolStart(294000, "TodoWrite", "", "write the migration")
	main.toolEnd(294500, "TodoWrite", "", "write the migration", nil, "")

	// Spawn order fixes the stable within-group ordering (§3.6).
	fix.spawn(8000, descFix)
	typ.spawn(10000, descType)
	tests.spawn(12000, descTests)
	audit.spawn(14000, descAudit)
	index.spawn(16000, descIndex)
	lint.spawn(18000, descLint)
	docs.spawn(26000, "waiting on write-email-index") // never starts: queued
	reviewer.spawn(28000, "named agent, limited telemetry")
	// docs and reviewer emit nothing further: queued and teammate rows.

	// fix-ts2345-fallout — ends ASK: three failing typechecks (two re-issues
	// feed the inferred dim x2 counter) interleaved with two edits so the
	// visible call window is the §3.1 one — ✔ Edit api/types.ts +29 −4,
	// ✖ Bash pnpm typecheck 1 err, ✔ Edit (src/)api/handlers.ts +6 −2 —
	// then blocked on the ask from t=248s (52s before the snapshot).
	fix.started(9000)
	fix.read(11000, "api/types.ts")
	fix.tokens(15000, 6000)
	tsErr := "TS2345 handlers.ts:88\n'string | null' not assignable to 'string'"
	fixCheck := func(ms int) {
		fix.toolStart(ms, "Bash", typecheckCmd, "typecheck the api package")
		fix.toolEnd(ms+25000, "Bash", typecheckCmd, "1 err TS2345 handlers.ts:88", code(1), tsErr)
	}
	fixCheck(30000)
	fix.tokens(60000, 21000)
	fixCheck(70000) // retry ↺1 (inferred)
	fix.tokens(100000, 29000)
	fix.edit(105000, 4000, "Edit", "api/types.ts", 29, 4)
	fixCheck(115000) // retry ↺2 (inferred), still failing
	fix.tokens(142000, 36000)
	fix.edit(150000, 4000, "Edit", "src/api/handlers.ts", 6, 2)
	for _, t := range []struct{ ms, k int }{
		{175000, 39}, {210000, 41}, {240000, 43}, {245000, 44},
	} {
		fix.tokens(t.ms, t.k*1000)
	}
	b.at(248000, askEvent(idFix))
	b.at(253000, askEvent(idGhost1)) // duplicate asks under fresh ids;
	b.at(259000, askEvent(idGhost2)) // the band dedupes to x3, ghosts fold.

	// typecheck-workspace — ends STUCK: edits, then silence from t=130s
	// (2m50s before the snapshot) with no call open.
	typ.started(13000)
	typ.read(16000, "tsconfig.json")
	typ.tokens(25000, 8000)
	typ.edit(40000, 5000, "Edit", "packages/api/tsconfig.json", 3, 1)
	typ.tokens(60000, 15000)
	typ.edit(80000, 5000, "Edit", "packages/worker/tsconfig.json", 2, 0)
	typ.tokens(100000, 26000)
	typ.tokens(124000, 33000)
	typ.edit(125000, 5000, "Edit", "src/types/session.ts", 11, 2)

	// run-auth-tests — ends RUN with an open call: one completed test run
	// (the basis for the inferred verified marker), then a background watch
	// open from t=108s (3m12s before the snapshot) kept alive by real
	// output growth (§2.6a).
	tests.started(15000)
	tests.read(18000, "src/auth/session.test.ts")
	tests.tokens(25000, 9000)
	tests.toolStart(35000, "Bash", "pnpm test --filter auth --run", "run the auth suite once")
	tests.toolEnd(95000, "Bash", "pnpm test --filter auth --run", "42 passed 0 failed", code(0), "")
	tests.tokens(97000, 27000)
	tests.tokens(106000, 38000)
	tests.toolStart(108000, "Bash", watchCmd, "keep the auth suite hot in watch mode")
	for _, o := range []struct {
		ms     int
		detail string
	}{
		{130000, "output +64 lines"}, {155000, "output +18 lines"},
		{180000, "output +42 lines"}, {205000, "output +12 lines"},
		{230000, "output +96 lines"}, {255000, "output +33 lines"},
		{280000, "output +27 lines"}, {295000, "output +51 lines"},
	} {
		tests.ev(o.ms, event.Event{Kind: event.ToolOutput, Tool: "Bash", Target: watchOutput, Detail: o.detail})
	}

	// audit-users-schema — RUN: grepping createUser, edited
	// src/db/schema.ts (one half of the contention pair).
	audit.started(17000)
	audit.read(20000, "src/db/schema.ts")
	audit.tokens(30000, 11000)
	audit.toolStart(50000, "Grep", `"createUser" src/**`, "")
	audit.toolEnd(52000, "Grep", `"createUser" src/**`, "9 matches", nil, "")
	audit.edit(60000, 10000, "Edit", "src/db/schema.ts", 12, 3)
	audit.tokens(90000, 24000)
	audit.toolStart(120000, "Grep", `"editUser" src/**`, "")
	audit.toolEnd(122000, "Grep", `"editUser" src/**`, "3 matches", nil, "")
	audit.edit(150000, 10000, "Edit", "src/db/users.ts", 18, 6)
	audit.tokens(200000, 39000)
	audit.tokens(230000, 45000)
	audit.read(240000, "src/db/indexes.ts")
	audit.tokens(265000, 48000)
	audit.tokens(290000, 52000)
	audit.toolStart(295000, "Grep", `"createUser" src/**`, "")
	audit.toolEnd(297000, "Grep", `"createUser" src/**`, "14 matches", nil, "")

	// write-email-index — RUN: also edits src/db/schema.ts (contention),
	// latest edit is 0042.sql +84 -3.
	index.started(19000)
	index.read(22000, "src/db/schema.ts")
	index.tokens(35000, 14000)
	index.edit(60000, 8000, "Edit", "src/db/schema.ts", 9, 1)
	index.tokens(95000, 26000)
	index.edit(110000, 8000, "Write", "db/migrations/0042.sql", 40, 0)
	index.tokens(150000, 52000)
	index.read(175000, "src/db/indexes.ts")
	index.tokens(210000, 67000)
	index.edit(235000, 9000, "Edit", "db/migrations/0042.sql", 30, 2)
	index.tokens(270000, 80000)
	index.edit(288000, 3000, "Edit", "db/migrations/0042.sql", 84, 3)
	index.tokens(292000, 86000)

	// lint-and-format — DONE: returns at t=254s, 46s before the snapshot,
	// so it has decayed (>30s) by then.
	lint.started(21000)
	lint.toolStart(24000, "Bash", "pnpm lint --fix", "lint the changed files")
	lint.toolEnd(60000, "Bash", "pnpm lint --fix", "0 errors 61 files", code(0), "")
	lint.tokens(65000, 12000)
	lint.edit(75000, 7000, "Edit", "src/auth/handlers.ts", 4, 4)
	lint.tokens(110000, 23000)
	lint.toolStart(150000, "Bash", "pnpm format", "format the tree")
	lint.toolEnd(190000, "Bash", "pnpm format", "61 files formatted", code(0), "")
	lint.tokens(220000, 41000)
	lint.tokens(250000, 47000)
	lint.ev(254000, event.Event{Kind: event.AgentReturned, Detail: "formatted 61 files, lint clean"})

	return b.events()
}
