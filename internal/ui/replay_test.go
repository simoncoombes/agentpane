package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/slug"
	"github.com/simoncoombes/agentpane/internal/source"
	"github.com/simoncoombes/agentpane/internal/source/hooksrc"
	"github.com/simoncoombes/agentpane/internal/state"
)

// This file replays a fixture shaped like real session
// 6dbbfaa6-71d1-412d-88ba-60f3ddf574bc through the whole stack — hook payloads
// → hooksrc → mux → state machine → rendered frame — and pins the two failures
// that session exposed:
//
//  1. every row was called "agent", "agent-2" … "agent-37", because the only
//     hook payload carrying a description (the parent's Agent PreToolUse) has
//     no agent_id, and the mux suppressed the transcript events that do carry
//     both;
//  2. the tree showed 37+ rows for 5 real subagents, because Claude Code also
//     emits SubagentStart/Stop for entities that do nothing at all.

const (
	fixtureSession = "6dbbfaa6-71d1-412d-88ba-60f3ddf574bc"
	fixtureType    = "Explore"
	phantomCount   = 40
)

// fixtureAgent is one real subagent of the replayed session: the description
// the parent's Agent call carried, and the id its SubagentStart carried.
type fixtureAgent struct {
	id   string
	desc string
	// probe is the file the agent reads, so it has observable work.
	probe string
}

func fixtureAgents() []fixtureAgent {
	return []fixtureAgent{
		{"a1b2c3d4e5f60718a", "Explore ACL in unstructured-ingest", "/src/ingest/acl.py"},
		{"b2c3d4e5f60718a2b", "Explore workflow types in platform-api", "/src/api/workflow.go"},
		{"c3d4e5f60718a2b3c", "Explore hook payload capture paths", "/src/hooks/capture.ts"},
		{"d4e5f60718a2b3c4d", "Explore session registry schema", "/src/registry/schema.json"},
		{"e5f60718a2b3c4d5e", "Explore transcript sidecar layout", "/src/transcript/meta.md"},
	}
}

func phantomAgentID(i int) string {
	return fmt.Sprintf("f%016x", i)
}

// hookLines builds the session's hook payloads in emission order: the prompt,
// five Agent calls issued in ONE assistant turn (no agent_id, description
// only), the forty phantom start/stop pairs the teams runtime emits, then the
// five real SubagentStarts (agent_id only) and their tool activity.
func hookLines() []string {
	agents := fixtureAgents()
	out := []string{
		fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionStart","source":"startup"}`, fixtureSession),
		fmt.Sprintf(`{"session_id":%q,"hook_event_name":"UserPromptSubmit","prompt":"Explore the connector stack"}`, fixtureSession),
	}
	for i, a := range agents {
		out = append(out, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":%q,"prompt":"…","subagent_type":%q},"tool_use_id":"toolu_spawn_%d"}`,
			fixtureSession, a.desc, fixtureType, i))
	}
	for i := 0; i < phantomCount; i++ {
		id := phantomAgentID(i)
		out = append(out,
			fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":"general-purpose"}`, fixtureSession, id),
			fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStop","agent_id":%q,"agent_type":"general-purpose","last_assistant_message":"","background_tasks":[]}`, fixtureSession, id),
		)
	}
	for _, a := range agents {
		out = append(out, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":%q}`,
			fixtureSession, a.id, fixtureType))
	}
	for i, a := range agents {
		out = append(out,
			fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","agent_id":%q,"agent_type":%q,"tool_name":"Read","tool_input":{"file_path":%q},"tool_use_id":"toolu_read_%d"}`,
				fixtureSession, a.id, fixtureType, a.probe, i),
			fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUse","agent_id":%q,"agent_type":%q,"tool_name":"Read","tool_input":{"file_path":%q},"tool_use_id":"toolu_read_%d","duration_ms":31}`,
				fixtureSession, a.id, fixtureType, a.probe, i),
		)
	}
	return out
}

// hookEvents maps the fixture's payloads exactly as the socket listener does:
// one Mapper, in order, stamped at ingest.
func hookEvents(t *testing.T, base time.Time) []event.Event {
	t.Helper()
	m := hooksrc.NewMapper()
	var out []event.Event
	for i, ln := range hookLines() {
		evs, err := m.Map([]byte(ln))
		if err != nil {
			t.Fatalf("mapping hook line %d: %v", i, err)
		}
		for _, ev := range evs {
			ev.At = base.Add(time.Duration(i) * 10 * time.Millisecond)
			out = append(out, ev)
		}
	}
	return out
}

// transcriptEvents is what the transcript source produces for the same
// session: one AgentSpawned per agent-<id>.meta.json sidecar, carrying the
// description and agentType, plus the per-agent token totals only it sees.
// The phantoms have no sidecar and no jsonl, so they appear here not at all —
// which is exactly what makes them phantoms.
func transcriptEvents(base time.Time) []event.Event {
	var out []event.Event
	for i, a := range fixtureAgents() {
		at := base.Add(time.Duration(i)*10*time.Millisecond + 2*time.Second)
		out = append(out,
			event.Event{At: at, SessionID: fixtureSession, AgentID: a.id, Kind: event.AgentSpawned,
				AgentName: a.desc, AgentType: fixtureType},
			event.Event{At: at, SessionID: fixtureSession, AgentID: a.id, Kind: event.TokensUpdated,
				AgentName: a.desc, AgentType: fixtureType, Tokens: 1200 * (i + 1), TokensCum: true},
		)
	}
	return out
}

// sliceSource replays a fixed event slice and closes, optionally waiting for
// another source to finish first so the merge order is deterministic.
type sliceSource struct {
	name string
	evs  []event.Event
	wait <-chan struct{}
	done chan struct{}
}

func newSliceSource(name string, evs []event.Event) *sliceSource {
	return &sliceSource{name: name, evs: evs, done: make(chan struct{})}
}

func (s *sliceSource) Name() string { return s.name }

func (s *sliceSource) Events(ctx context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event)
	go func() {
		defer close(ch)
		defer close(s.done)
		if s.wait != nil {
			select {
			case <-s.wait:
			case <-ctx.Done():
				return
			}
		}
		for _, ev := range s.evs {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// replay runs the given sources through the real mux into a real machine and
// returns the machine plus every event that reached it.
func replay(t *testing.T, sources ...event.Source) (*state.Machine, []event.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, err := source.Mux(ctx, sources...)
	if err != nil {
		t.Fatalf("Mux: %v", err)
	}
	m := state.New(state.DefaultConfig())
	var seen []event.Event
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return m, seen
			}
			m.Apply(ev, ev.At)
			seen = append(seen, ev)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out draining the mux")
		}
	}
}

// replayView renders the world at now and returns the frame plus the world.
func replayView(t *testing.T, m *state.Machine, now time.Time) (string, state.World, *UIState) {
	t.Helper()
	m.Tick(now)
	w := m.Snapshot()
	v := newUIState(testConfig())
	frame, _ := renderFrame(w, v, 64, 44, now, NewPalette(2, false))
	return stripANSI(frame), w, v
}

// branchLines counts rendered agent blocks: agentBlock opens each with exactly
// one branch glyph line, so this is the number of rows the tree actually shows.
func branchLines(frame string) int {
	n := 0
	for _, ln := range strings.Split(frame, "\n") {
		switch strings.TrimSpace(ln) {
		case branchMid, branchLast:
			n++
		}
	}
	return n
}

var numberedAgentRe = regexp.MustCompile(`\bagent-\d+\b`)

// TestReplay6dbbfaa6HooksAndTranscript is acceptance criterion 1: the full
// fixture through both sources yields exactly five named rows and forty
// suppressed agents reported as a count.
func TestReplay6dbbfaa6HooksAndTranscript(t *testing.T) {
	base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	hooks := newSliceSource("hooks", hookEvents(t, base))
	tr := newSliceSource("transcript", transcriptEvents(base))
	tr.wait = hooks.done // transcript lands after the hooks, as it does live

	m, seen := replay(t, hooks, tr)
	now := base.Add(10 * time.Second)
	frame, w, _ := replayView(t, m, now)

	if len(w.Agents) != 5 {
		t.Fatalf("rendered agents = %d, want 5\n%s", len(w.Agents), frame)
	}
	if len(w.Quiet) != phantomCount {
		t.Fatalf("suppressed agents = %d, want %d", len(w.Quiet), phantomCount)
	}
	if got := branchLines(frame); got != 5 {
		t.Fatalf("tree drew %d rows, want 5\n%s", got, frame)
	}

	// Every row carries its real, description-derived name.
	byID := map[string]state.Agent{}
	for _, a := range w.Agents {
		byID[a.ID] = a
	}
	tbl := slug.New(testConfig().SlugMax)
	for _, fa := range fixtureAgents() {
		a, ok := byID[fa.id]
		if !ok {
			t.Fatalf("agent %s missing from the tree", fa.id)
		}
		if a.Name != fa.desc {
			t.Errorf("agent %s: Name = %q, want %q", fa.id, a.Name, fa.desc)
		}
		want := tbl.Assign(fa.id, fa.desc)
		if !strings.Contains(frame, want) {
			t.Errorf("slug %q missing from the frame\n%s", want, frame)
		}
	}
	if loc := numberedAgentRe.FindString(frame); loc != "" {
		t.Errorf("a nameless numbered row survived: %q\n%s", loc, frame)
	}

	// The header counts what the tree shows; the remainder is stated once.
	if !strings.Contains(frame, "AGENTS 5") {
		t.Errorf("header does not agree with the five rendered rows\n%s", frame)
	}
	quietLine := fmt.Sprintf("+%d agents with no recorded activity", phantomCount)
	if !strings.Contains(frame, quietLine) {
		t.Errorf("suppressed agents were not reported (%q missing)\n%s", quietLine, frame)
	}

	// Acceptance criterion 4: the Agent tool calls belong to main and created
	// nothing keyed to it.
	for _, ev := range seen {
		if ev.Kind == event.AgentSpawned && ev.AgentID == event.MainAgentID {
			t.Fatalf("an AgentSpawned keyed to main reached the machine: %+v", ev)
		}
	}
	if _, ok := byID[event.MainAgentID]; ok {
		t.Error("main appeared as a subagent row")
	}
	for _, q := range w.Quiet {
		if q.ID == event.MainAgentID {
			t.Error("main was counted as a suppressed subagent")
		}
	}

	// Real work survived the late-arriving names.
	for _, a := range w.Agents {
		if a.Station < 1 {
			t.Errorf("agent %s lost its observed station", a.ID)
		}
		if a.Tokens == 0 {
			t.Errorf("agent %s lost its transcript token count", a.ID)
		}
	}
}

// TestReplay6dbbfaa6HooksOnly is acceptance criterion 2: with no transcript
// source there are no descriptions at all, and the rows must still not fall
// back to "agent-N". The documented fallback is the agent's own reported
// agent_type plus the head of its id.
func TestReplay6dbbfaa6HooksOnly(t *testing.T) {
	base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	hooks := newSliceSource("hooks", hookEvents(t, base))

	m, _ := replay(t, hooks)
	now := base.Add(10 * time.Second)
	frame, w, _ := replayView(t, m, now)

	if len(w.Agents) != 5 {
		t.Fatalf("rendered agents = %d, want 5\n%s", len(w.Agents), frame)
	}
	if len(w.Quiet) != phantomCount {
		t.Fatalf("suppressed agents = %d, want %d", len(w.Quiet), phantomCount)
	}
	if got := branchLines(frame); got != 5 {
		t.Fatalf("tree drew %d rows, want 5\n%s", got, frame)
	}
	if loc := numberedAgentRe.FindString(frame); loc != "" {
		t.Fatalf("hooks-only rows regressed to %q\n%s", loc, frame)
	}
	// Five spawns were issued in one turn, so the hook correlation is
	// ambiguous by construction and declines to name anyone (C8).
	for _, a := range w.Agents {
		if a.Name != "" {
			t.Errorf("agent %s got a guessed name %q from hooks alone", a.ID, a.Name)
		}
		want := placeholderName(a.ID, a.Type)
		if !strings.Contains(frame, want) {
			t.Errorf("fallback name %q missing from the frame\n%s", want, frame)
		}
	}
	if !strings.Contains(frame, fmt.Sprintf("+%d agents with no recorded activity", phantomCount)) {
		t.Errorf("suppressed agents were not reported\n%s", frame)
	}
}

// TestReplayNameUpgradesFromPlaceholder: the placeholder must not burn the
// slug. A frame painted before the transcript sidecar lands shows the
// fallback; the next frame shows the real §3.16 slug, on the same UIState.
func TestReplayNameUpgradesFromPlaceholder(t *testing.T) {
	base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	fa := fixtureAgents()[0]

	m := state.New(state.DefaultConfig())
	v := newUIState(testConfig())
	apply := func(ev event.Event) {
		ev.SessionID = fixtureSession
		m.Apply(ev, ev.At)
	}
	apply(event.Event{Seq: 1, At: base, AgentID: fa.id, Kind: event.AgentStarted, AgentType: fixtureType})
	apply(event.Event{Seq: 2, At: base.Add(time.Second), AgentID: fa.id, Kind: event.FileRead,
		Tool: "Read", Target: fa.probe})

	now := base.Add(2 * time.Second)
	first, _ := renderFrame(m.Snapshot(), v, 64, 44, now, NewPalette(2, false))
	first = stripANSI(first)
	placeholder := placeholderName(fa.id, fixtureType)
	if !strings.Contains(first, placeholder) {
		t.Fatalf("unnamed row is not showing the documented placeholder %q\n%s", placeholder, first)
	}

	// The sidecar lands one poll later.
	apply(event.Event{Seq: 3, At: base.Add(3 * time.Second), AgentID: fa.id, Kind: event.AgentSpawned,
		AgentName: fa.desc, AgentType: fixtureType})

	now = base.Add(4 * time.Second)
	second, _ := renderFrame(m.Snapshot(), v, 64, 44, now, NewPalette(2, false))
	second = stripANSI(second)
	want := slug.New(testConfig().SlugMax).Assign(fa.id, fa.desc)
	if !strings.Contains(second, want) {
		t.Fatalf("the real name never reached the row (want %q)\n%s", want, second)
	}
	if strings.Contains(second, placeholder) {
		t.Errorf("the placeholder outlived the name it stood in for\n%s", second)
	}
}

// TestReplayPhantomNeverStealsARealName is FINDING 1 end to end, in the two
// orderings the audit reproduced live: a phantom SubagentStart interleaved with
// a real spawn must not take the real child's description, the real child must
// be named once it corroborates, and the phantom must never render at all.
func TestReplayPhantomNeverStealsARealName(t *testing.T) {
	const (
		desc      = "Explore ACL in unstructured-ingest"
		phantomID = "aphantom000000000"
		realID    = "a1b2c3d4e5f60718a"
	)
	for _, tc := range []struct {
		name        string
		phantomType string
	}{
		{"phantom with no agent_type", ""},
		{"phantom with the same agent_type", fixtureType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
			lines := []string{
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionStart","source":"startup"}`, fixtureSession),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"UserPromptSubmit","prompt":"Explore the connector stack"}`, fixtureSession),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":%q,"prompt":"…","subagent_type":%q},"tool_use_id":"toolu_spawn_0"}`,
					fixtureSession, desc, fixtureType),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":%q}`,
					fixtureSession, phantomID, tc.phantomType),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":%q}`,
					fixtureSession, realID, fixtureType),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","agent_id":%q,"agent_type":%q,"tool_name":"Read","tool_input":{"file_path":"/src/ingest/acl.py"},"tool_use_id":"toolu_read_0"}`,
					fixtureSession, realID, fixtureType),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUse","agent_id":%q,"agent_type":%q,"tool_name":"Read","tool_input":{"file_path":"/src/ingest/acl.py"},"tool_use_id":"toolu_read_0","duration_ms":31}`,
					fixtureSession, realID, fixtureType),
			}
			m := hooksrc.NewMapper()
			var evs []event.Event
			for i, ln := range lines {
				got, err := m.Map([]byte(ln))
				if err != nil {
					t.Fatalf("mapping line %d: %v", i, err)
				}
				for _, ev := range got {
					ev.At = base.Add(time.Duration(i) * 10 * time.Millisecond)
					evs = append(evs, ev)
				}
			}
			mach, _ := replay(t, newSliceSource("hooks", evs))

			now := base.Add(10 * time.Second)
			frame, w, _ := replayView(t, mach, now)
			if len(w.Agents) != 1 || w.Agents[0].ID != realID {
				t.Fatalf("rendered agents = %+v, want only the working one\n%s", w.Agents, frame)
			}
			if w.Agents[0].Name != desc {
				t.Errorf("the working agent is called %q, want %q", w.Agents[0].Name, desc)
			}
			if len(w.Quiet) != 1 || w.Quiet[0].ID != phantomID {
				t.Fatalf("quiet = %+v, want the phantom counted there", w.Quiet)
			}
			want := slug.New(testConfig().SlugMax).Assign(realID, desc)
			if !strings.Contains(frame, want) {
				t.Errorf("the real agent's slug %q is missing\n%s", want, frame)
			}
			if got := branchLines(frame); got != 1 {
				t.Fatalf("tree drew %d rows, want 1\n%s", got, frame)
			}
			// The phantom is on no row, under any name it could have.
			for _, ph := range []string{placeholderName(phantomID, tc.phantomType), phantomID[:8]} {
				if strings.Contains(frame, ph) {
					t.Errorf("a phantom took a row as %q\n%s", ph, frame)
				}
			}

			// It settles, and so does the real agent: no row may disappear.
			settle := []string{
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStop","agent_id":%q,"agent_type":%q,"last_assistant_message":"nothing to report"}`,
					fixtureSession, phantomID, tc.phantomType),
				fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStop","agent_id":%q,"agent_type":%q,"last_assistant_message":"found it"}`,
					fixtureSession, realID, fixtureType),
			}
			for i, ln := range settle {
				got, err := m.Map([]byte(ln))
				if err != nil {
					t.Fatalf("mapping settle line %d: %v", i, err)
				}
				for _, ev := range got {
					ev.At = now
					ev.Seq = uint64(1000 + i)
					mach.Apply(ev, now)
				}
			}
			after, w2, _ := replayView(t, mach, now.Add(time.Second))
			if got := branchLines(after); got != 1 {
				t.Fatalf("rows changed on settle: %d, want the 1 that was on screen\n%s", got, after)
			}
			if len(w2.Agents) != 1 || w2.Agents[0].ID != realID {
				t.Fatalf("the settled row vanished: %+v (quiet %+v)", w2.Agents, w2.Quiet)
			}
			if !strings.Contains(after, want) {
				t.Errorf("the row lost its name on settle\n%s", after)
			}
		})
	}
}

// TestReplayQuietRowExpands: the suppressed count is selectable and names the
// agents behind it, so nothing is invisible (§1.5).
func TestReplayQuietRowExpands(t *testing.T) {
	base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	hooks := newSliceSource("hooks", hookEvents(t, base))
	m, _ := replay(t, hooks)
	now := base.Add(10 * time.Second)
	m.Tick(now)
	w := m.Snapshot()

	v := newUIState(testConfig())
	ids := v.selectionIDs(w, false, quietLineDrawn(w, 64))
	found := false
	for _, id := range ids {
		if id == selQuiet {
			found = true
		}
	}
	if !found {
		t.Fatalf("the suppressed-agents line is not reachable with j/k: %v", ids)
	}

	v.SelID = selQuiet
	frame, _ := renderFrame(w, v, 64, 44, now, NewPalette(2, false))
	frame = stripANSI(frame)
	first := placeholderName(w.Quiet[0].ID, w.Quiet[0].Type)
	if !strings.Contains(frame, first) {
		t.Errorf("expanding the suppressed line did not name %q\n%s", first, frame)
	}
	if !strings.Contains(frame, fmt.Sprintf("+%d more", phantomCount-quietPreview)) {
		t.Errorf("the expanded list did not account for the rest\n%s", frame)
	}
	if branchLines(frame) != 5 {
		t.Errorf("expanding the suppressed line changed the agent rows\n%s", frame)
	}
}
