package hooksrc

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// spawnPayload builds a parent PreToolUse for the Agent tool: description and
// subagent_type, no agent_id (DATA-SOURCES §3.3 "PreToolUse — main agent").
func spawnPayload(session, desc, typ, toolUseID string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"description":%q,"prompt":"…","subagent_type":%q},"tool_use_id":%q}`,
		session, desc, typ, toolUseID)
}

// startPayload builds a SubagentStart: agent_id and agent_type, no
// description and no tool_use_id (DATA-SOURCES §3.3 "SubagentStart").
func startPayload(session, agentID, typ string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":%q}`,
		session, agentID, typ)
}

// workPayload is the agent's own first tool call: the corroboration a deferred
// claim waits for (see Mapper).
func workPayload(session, agentID, typ string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","agent_id":%q,"agent_type":%q,"tool_name":"Read","tool_input":{"file_path":"/src/x.go"},"tool_use_id":"toolu_read_%s"}`,
		session, agentID, typ, agentID)
}

// stopPayload builds a SubagentStop; transcript is agent_transcript_path,
// which only a real agent has.
func stopPayload(session, agentID, typ, transcript string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStop","agent_id":%q,"agent_type":%q,"agent_transcript_path":%q,"last_assistant_message":"done"}`,
		session, agentID, typ, transcript)
}

// mapAll maps every payload through one mapper, in order, as the socket does.
func mapAll(t *testing.T, m *Mapper, payloads ...string) []event.Event {
	t.Helper()
	var out []event.Event
	for _, p := range payloads {
		evs, err := m.Map([]byte(p))
		if err != nil {
			t.Fatalf("Map(%s): %v", p, err)
		}
		out = append(out, evs...)
	}
	return out
}

// started returns the AgentStarted events, keyed by agent id.
func started(evs []event.Event) map[string]event.Event {
	out := map[string]event.Event{}
	for _, ev := range evs {
		if ev.Kind == event.AgentStarted {
			out[ev.AgentID] = ev
		}
	}
	return out
}

// names is the name each agent ends up with: whatever any naming event
// (AgentStarted's own agent_name, or a corroborated AgentSpawned) carried.
func names(evs []event.Event) map[string]string {
	out := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case event.AgentStarted, event.AgentSpawned:
			if ev.AgentName != "" {
				out[ev.AgentID] = ev.AgentName
			} else if _, seen := out[ev.AgentID]; !seen {
				out[ev.AgentID] = ""
			}
		}
	}
	return out
}

// TestSpawnCorrelationSequential is the common case: one Agent call in flight
// at a time, so the join is unambiguous and hooks alone can name the row —
// once each child has corroborated that it is real.
func TestSpawnCorrelationSequential(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		spawnPayload("s", "Explore workflow types in platform-api", "Explore", "toolu_2"),
		startPayload("s", "b2c3d4e5f60718a2b", "Explore"),
		workPayload("s", "b2c3d4e5f60718a2b", "Explore"),
	)
	got := names(evs)
	want := map[string]string{
		"a1b2c3d4e5f60718a": "Explore ACL in unstructured-ingest",
		"b2c3d4e5f60718a2b": "Explore workflow types in platform-api",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("agent %s: name = %q, want %q", id, got[id], w)
		}
	}
}

// TestSpawnCorrelationInterleaved: the second Agent call is issued while the
// first child is still working. The later spawn was issued AFTER that child
// started, so it can never be its name — the watermark keeps the sequential
// case unambiguous even when the claims are deferred.
func TestSpawnCorrelationInterleaved(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		spawnPayload("s", "Explore workflow types in platform-api", "Explore", "toolu_2"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"), // corroborates AFTER spawn 2
		startPayload("s", "b2c3d4e5f60718a2b", "Explore"),
		workPayload("s", "b2c3d4e5f60718a2b", "Explore"),
	)
	got := names(evs)
	if n := got["a1b2c3d4e5f60718a"]; n != "Explore ACL in unstructured-ingest" {
		t.Errorf("first agent: name = %q, want its own spawn's description", n)
	}
	if n := got["b2c3d4e5f60718a2b"]; n != "Explore workflow types in platform-api" {
		t.Errorf("second agent: name = %q, want its own spawn's description", n)
	}
}

// TestSpawnPhantomStartCannotClaim is the FINDING 1 case, both orderings the
// audit reproduced live: a phantom SubagentStart interleaved with a real spawn
// must never take the real child's description, and the real child must still
// end up correctly named once it corroborates.
func TestSpawnPhantomStartCannotClaim(t *testing.T) {
	const (
		desc    = "Explore ACL in unstructured-ingest"
		phantom = "aPHANTOM000000000"
		real_   = "a1b2c3d4e5f60718a"
	)
	for _, tc := range []struct {
		name        string
		phantomType string
	}{
		{"phantom with no agent_type", ""},
		{"phantom with the same agent_type", "Explore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMapper()
			evs := mapAll(t, m,
				spawnPayload("s", desc, "Explore", "toolu_1"),
				startPayload("s", phantom, tc.phantomType),
				startPayload("s", real_, "Explore"),
				workPayload("s", real_, "Explore"),
				// The phantom settles having done nothing, with no transcript.
				fmt.Sprintf(`{"session_id":"s","hook_event_name":"SubagentStop","agent_id":%q,"agent_type":%q,"last_assistant_message":"ok"}`, phantom, tc.phantomType),
			)
			got := names(evs)
			if n := got[phantom]; n != "" {
				t.Errorf("the phantom was given the real child's name %q", n)
			}
			if n := got[real_]; n != desc {
				t.Errorf("the working agent: name = %q, want %q", n, desc)
			}
			for _, ev := range evs {
				if ev.AgentID == phantom && ev.Kind == event.AgentSpawned {
					t.Errorf("a phantom produced a spawn record: %+v", ev)
				}
			}
		})
	}
}

// TestSpawnBareStartNeverClaims: a start with nothing behind it stays unnamed,
// however unambiguous the pending queue looks.
func TestSpawnBareStartNeverClaims(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "" {
		t.Errorf("a bare SubagentStart claimed %q", n)
	}
	for _, ev := range evs {
		if ev.Kind == event.AgentSpawned {
			t.Errorf("a bare SubagentStart produced a spawn record: %+v", ev)
		}
	}
}

// TestSpawnEmptyAgentTypeNeverClaims: an empty agent_type says nothing about
// which call this is, so it declines even when it corroborates.
func TestSpawnEmptyAgentTypeNeverClaims(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", ""),
		workPayload("s", "a1b2c3d4e5f60718a", ""),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "" {
		t.Errorf("a typeless start claimed %q", n)
	}
	// The description is still available for the agent it really belongs to.
	evs = mapAll(t, m,
		startPayload("s", "b2c3d4e5f60718a2b", "Explore"),
		workPayload("s", "b2c3d4e5f60718a2b", "Explore"),
	)
	if n := names(evs)["b2c3d4e5f60718a2b"]; n != "Explore ACL in unstructured-ingest" {
		t.Errorf("the real agent lost its name to a typeless start: %q", n)
	}
}

// TestSpawnCorroborationViaTranscriptPath: an agent that ran no tool but wrote
// a transcript is real, and its SubagentStop says so.
func TestSpawnCorroborationViaTranscriptPath(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Summarise the diff", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		stopPayload("s", "a1b2c3d4e5f60718a", "Explore", "/tmp/subagents/agent-a1b2c3d4e5f60718a.jsonl"),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "Summarise the diff" {
		t.Errorf("name = %q, want the description; a transcript sidecar corroborates", n)
	}

	// A stop with no transcript path is not corroboration.
	m2 := NewMapper()
	evs = mapAll(t, m2,
		spawnPayload("s", "Summarise the diff", "Explore", "toolu_1"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		stopPayload("s", "a1b2c3d4e5f60718a", "Explore", ""),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "" {
		t.Errorf("a transcript-less stop claimed %q", n)
	}
}

// TestSpawnClaimsOnce: the second and third tool calls of a named agent must
// not consume further spawns.
func TestSpawnClaimsOnce(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		spawnPayload("s", "Review the diff", "code-reviewer", "toolu_2"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
	)
	spawns := 0
	for _, ev := range evs {
		if ev.Kind == event.AgentSpawned {
			spawns++
		}
	}
	if spawns != 1 {
		t.Errorf("corroboration produced %d spawn records, want exactly 1", spawns)
	}
	if n := len(m.sess["s"].pending); n != 1 {
		t.Errorf("pending spawns = %d, want the unrelated code-reviewer call left alone", n)
	}
}

// TestSpawnCorrelationAmbiguous is the C8 case: two Agent calls issued in one
// assistant turn, two SubagentStarts, and nothing in either payload saying
// which is which. Neither agent may be given a name — and above all, neither
// may be given the OTHER's name.
func TestSpawnCorrelationAmbiguous(t *testing.T) {
	m := NewMapper()
	const (
		descA = "Explore ACL in unstructured-ingest"
		descB = "Explore workflow types in platform-api"
		idA   = "a1b2c3d4e5f60718a"
		idB   = "b2c3d4e5f60718a2b"
	)
	evs := mapAll(t, m,
		spawnPayload("s", descA, "Explore", "toolu_1"),
		spawnPayload("s", descB, "Explore", "toolu_2"),
		startPayload("s", idA, "Explore"),
		startPayload("s", idB, "Explore"),
		workPayload("s", idA, "Explore"),
		workPayload("s", idB, "Explore"),
	)
	got := names(evs)
	if len(started(evs)) != 2 {
		t.Fatalf("got %d AgentStarted events, want 2", len(started(evs)))
	}
	for _, id := range []string{idA, idB} {
		switch got[id] {
		case "":
			// Correct: no name beats a wrong name.
		case descA, descB:
			t.Errorf("agent %s was given a guessed name %q — two spawns were in flight and nothing said which was which",
				id, got[id])
		default:
			t.Errorf("agent %s got an invented name %q", id, got[id])
		}
	}
	// A third start must not inherit a burned candidate either.
	more := names(mapAll(t, m,
		startPayload("s", "c3d4e5f60718a2b3c", "Explore"),
		workPayload("s", "c3d4e5f60718a2b3c", "Explore"),
	))
	if n := more["c3d4e5f60718a2b3c"]; n != "" {
		t.Errorf("a later start picked up a burned spawn name %q", n)
	}
}

// TestSpawnCorrelationTypeDisambiguates: two spawns in flight are NOT
// ambiguous when their subagent_types differ and the start names one of them.
func TestSpawnCorrelationTypeDisambiguates(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		spawnPayload("s", "Review the diff", "code-reviewer", "toolu_2"),
		startPayload("s", "b2c3d4e5f60718a2b", "code-reviewer"),
		workPayload("s", "b2c3d4e5f60718a2b", "code-reviewer"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
	)
	got := names(evs)
	if n := got["b2c3d4e5f60718a2b"]; n != "Review the diff" {
		t.Errorf("code-reviewer name = %q, want %q", n, "Review the diff")
	}
	if n := got["a1b2c3d4e5f60718a"]; n != "Explore ACL in unstructured-ingest" {
		t.Errorf("Explore name = %q, want %q", n, "Explore ACL in unstructured-ingest")
	}
}

// TestSpawnCorrelationIdenticalDescriptions: several in-flight spawns that
// share a description are ambiguous in identity but not in name, so each
// corroborated start may take it — and exactly one spawn is consumed per start.
func TestSpawnCorrelationIdenticalDescriptions(t *testing.T) {
	m := NewMapper()
	const desc = "Run the same probe"
	evs := mapAll(t, m,
		spawnPayload("s", desc, "Explore", "toolu_1"),
		spawnPayload("s", desc, "Explore", "toolu_2"),
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		startPayload("s", "b2c3d4e5f60718a2b", "Explore"),
		workPayload("s", "b2c3d4e5f60718a2b", "Explore"),
		startPayload("s", "c3d4e5f60718a2b3c", "Explore"),
		workPayload("s", "c3d4e5f60718a2b3c", "Explore"),
	)
	got := names(evs)
	for _, id := range []string{"a1b2c3d4e5f60718a", "b2c3d4e5f60718a2b"} {
		if got[id] != desc {
			t.Errorf("agent %s: name = %q, want %q", id, got[id], desc)
		}
	}
	if n := got["c3d4e5f60718a2b3c"]; n != "" {
		t.Errorf("third start took a name from an exhausted queue: %q", n)
	}
}

// TestSpawnCorrelationSessionScoped: a spawn issued in one session can never
// name an agent starting in another.
func TestSpawnCorrelationSessionScoped(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("session-A", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		startPayload("session-B", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("session-B", "a1b2c3d4e5f60718a", "Explore"),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "" {
		t.Errorf("cross-session name leak: %q", n)
	}
}

// TestSpawnCorrelationNamedAgentWins: a SubagentStart that carries agent_name
// keeps it; the correlation queue is only consulted when the payload has none.
func TestSpawnCorrelationNamedAgentWins(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m,
		spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"),
		`{"session_id":"s","hook_event_name":"SubagentStart","agent_id":"a1b2c3d4e5f60718a","agent_type":"Explore","agent_name":"reviewer"}`,
	)
	if n := started(evs)["a1b2c3d4e5f60718a"].AgentName; n != "reviewer" {
		t.Errorf("AgentName = %q, want the payload's own agent_name", n)
	}
}

// TestSpawnCorrelationExpiryDrops: a spawn that goes correlationTTL unclaimed
// is dropped, not handed to whoever corroborates next.
func TestSpawnCorrelationExpiryDrops(t *testing.T) {
	m := NewMapper()
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }

	mapAll(t, m, spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"))
	now = now.Add(correlationTTL + time.Second)
	evs := mapAll(t, m,
		startPayload("s", "a1b2c3d4e5f60718a", "Explore"),
		workPayload("s", "a1b2c3d4e5f60718a", "Explore"),
	)
	if n := names(evs)["a1b2c3d4e5f60718a"]; n != "" {
		t.Errorf("an expired spawn was still assigned: %q", n)
	}
	if n := len(m.sess["s"].pending); n != 0 {
		t.Errorf("expired spawn still queued: %d", n)
	}

	// A start that never corroborates expires too, and claims nothing after.
	mapAll(t, m, spawnPayload("s", "Review the diff", "code-reviewer", "toolu_2"),
		startPayload("s", "b2c3d4e5f60718a2b", "code-reviewer"))
	now = now.Add(correlationTTL + time.Second)
	evs = mapAll(t, m, workPayload("s", "b2c3d4e5f60718a2b", "code-reviewer"))
	if n := names(evs)["b2c3d4e5f60718a2b"]; n != "" {
		t.Errorf("an expired start still claimed: %q", n)
	}
}

// TestSpawnQueueBounded: unclaimed spawns (a denied Agent call never starts)
// and uncorroborated starts (the ~40 phantoms a real session emits) must not
// grow without bound.
func TestSpawnQueueBounded(t *testing.T) {
	m := NewMapper()
	for i := 0; i < maxPendingSpawns*3; i++ {
		mapAll(t, m, spawnPayload("s", fmt.Sprintf("desc %d", i), "Explore", fmt.Sprintf("toolu_%d", i)))
	}
	if n := len(m.sess["s"].pending); n > maxPendingSpawns {
		t.Fatalf("pending queue grew to %d, cap is %d", n, maxPendingSpawns)
	}
	for i := 0; i < maxPendingStarts*3; i++ {
		mapAll(t, m, startPayload("s", fmt.Sprintf("phantom-%05d", i), "general-purpose"))
	}
	if n := len(m.sess["s"].starts); n > maxPendingStarts {
		t.Fatalf("waiting-start set grew to %d, cap is %d", n, maxPendingStarts)
	}
	for i := 0; i < maxPendingSessions*3; i++ {
		mapAll(t, m, spawnPayload(fmt.Sprintf("s%d", i), "desc", "Explore", "toolu"))
	}
	if n := len(m.sess); n > maxPendingSessions {
		t.Fatalf("pending session map grew to %d, cap is %d", n, maxPendingSessions)
	}
}

// TestSpawnCorrelationRawIntact: correlation must not disturb the debug ring's
// verbatim payload.
func TestSpawnCorrelationRawIntact(t *testing.T) {
	m := NewMapper()
	p := startPayload("s", "a1b2c3d4e5f60718a", "Explore")
	evs := mapAll(t, m, p)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if !json.Valid(evs[0].Raw) || string(evs[0].Raw) != p {
		t.Errorf("Raw = %s, want the original payload", evs[0].Raw)
	}
}

// TestAgentToolPreToolUseCreatesNothingKeyedToMain is acceptance criterion 4:
// the parent's Agent call is a tool call on main's row and nothing else. The
// old mapping emitted an AgentSpawned keyed to main, which named no agent and
// credited main with a spawn.
func TestAgentToolPreToolUseCreatesNothingKeyedToMain(t *testing.T) {
	m := NewMapper()
	evs := mapAll(t, m, spawnPayload("s", "Explore ACL in unstructured-ingest", "Explore", "toolu_1"))
	if len(evs) != 1 {
		t.Fatalf("got %d events, want exactly the ToolStart: %+v", len(evs), evs)
	}
	if evs[0].Kind != event.ToolStart || evs[0].Tool != "Agent" || evs[0].AgentID != event.MainAgentID {
		t.Fatalf("unexpected event: %+v", evs[0])
	}
	for _, ev := range evs {
		if ev.Kind == event.AgentSpawned {
			t.Fatalf("an Agent-tool PreToolUse produced an AgentSpawned keyed to %q", ev.AgentID)
		}
	}
	// The PostToolUse twin is likewise only main's tool call ending.
	post := fmt.Sprintf(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Agent","tool_input":{"description":%q},"tool_use_id":"toolu_1","duration_ms":12}`,
		"Explore ACL in unstructured-ingest")
	for _, ev := range mapAll(t, m, post) {
		if ev.Kind != event.ToolEnd {
			t.Fatalf("PostToolUse for Agent produced %v", ev.Kind)
		}
	}
}
