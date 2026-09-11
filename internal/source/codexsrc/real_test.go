package codexsrc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"bytes"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/ui"
)

// Against a REAL Codex session on this machine, when there is one. Skipped
// everywhere else, because a fixture of somebody's actual conversation is not a
// thing to commit — the synthesised tests below cover the mapping, and this
// covers the assumption that the mapping is of the right thing.
func TestRealCodexSession(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(home, ".codex", "sessions")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no local Codex sessions")
	}
	sessions, err := Scan(dir, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 {
		t.Skip("no sessions on disk")
	}
	var fanout *Session
	for i, s := range sessions {
		if len(s.Threads) > 1 {
			fanout = &sessions[i]
			break
		}
	}
	if fanout == nil {
		t.Skip("no session with subagents on disk")
	}

	src := New(dir, fanout.ID)
	evs := src.poll()
	kinds := map[event.Kind]int{}
	agents := map[string]string{}
	tokens := map[string]int{}
	for _, ev := range evs {
		kinds[ev.Kind]++
		if ev.Kind == event.AgentSpawned {
			agents[ev.AgentID] = ev.AgentName
		}
		if ev.Kind == event.TokensUpdated {
			tokens[ev.AgentID] = ev.Tokens
		}
	}
	t.Logf("session %s threads=%d events=%d", fanout.ID, len(fanout.Threads), len(evs))
	t.Logf("kinds=%v", kinds)
	t.Logf("agents=%v", agents)
	t.Logf("tokens=%v", tokens)

	if kinds[event.SessionStart] == 0 {
		t.Error("no SessionStart: the root thread was not recognised")
	}
	if kinds[event.AgentSpawned] != len(fanout.Threads)-1 {
		t.Errorf("%d spawns for %d subagent threads", kinds[event.AgentSpawned], len(fanout.Threads)-1)
	}
	if kinds[event.ToolStart] == 0 || kinds[event.ToolEnd] == 0 {
		t.Error("no tool calls: the work is not being read")
	}
	for id, name := range agents {
		if name == "" {
			t.Errorf("agent %s has no name", id)
		}
	}
}

// The whole way through: a real Codex session, the real state machine, the real
// renderer. If this draws a tree, the platform is supported — everything above
// event.Source was already platform-agnostic and this is the proof.
func TestRealCodexSessionRendersATree(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(home, ".codex", "sessions")
	sessions, err := Scan(dir, 0, time.Now())
	if err != nil || len(sessions) == 0 {
		t.Skip("no local Codex sessions")
	}
	var fanout *Session
	for i, s := range sessions {
		if len(s.Threads) > 1 {
			fanout = &sessions[i]
			break
		}
	}
	if fanout == nil {
		t.Skip("no session with subagents on disk")
	}

	mach := state.New(state.DefaultConfig())
	var last time.Time
	for _, ev := range New(dir, fanout.ID).poll() {
		mach.Apply(ev, ev.At)
		if ev.At.After(last) {
			last = ev.At
		}
	}
	mach.Tick(last)
	w := mach.Snapshot()
	if len(w.Agents) == 0 {
		t.Fatalf("no agents in the world: quiet=%d", len(w.Quiet))
	}
	for _, a := range w.Agents {
		t.Logf("agent %-12s name=%-14q model=%-12s tokens=%-7d calls=%d status=%s",
			a.ID[:8], a.Name, a.Model, a.Tokens, a.TotalCalls, a.Status)
	}
	t.Logf("main: model=%s tokens=%d", w.Main.Model, w.Main.Tokens)
	for _, a := range w.Agents {
		if a.Name == "" {
			t.Errorf("agent %s reached the tree with no name", a.ID)
		}
		if a.Model == "" {
			t.Errorf("agent %s reached the tree with no model", a.ID)
		}
	}
}

// And onto the screen: the real program, drawing a real Codex session.
func TestRealCodexSessionDrawsAFrame(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(home, ".codex", "sessions")
	sessions, err := Scan(dir, 0, time.Now())
	if err != nil || len(sessions) == 0 {
		t.Skip("no local Codex sessions")
	}
	var fanout *Session
	for i, s := range sessions {
		if len(s.Threads) > 1 {
			fanout = &sessions[i]
			break
		}
	}
	if fanout == nil {
		t.Skip("no session with subagents on disk")
	}

	evs := New(dir, fanout.ID).poll()
	var last time.Time
	for _, ev := range evs {
		if ev.At.After(last) {
			last = ev.At
		}
	}
	// Minus the session going idle at the end: this asserts what a Codex run
	// IN FLIGHT looks like, which is the state a monitor is for. (A finished
	// one correctly draws the idle screen instead, which is what the first
	// version of this test caught itself asserting against.)
	feed := make(chan event.Event, len(evs)+1)
	for _, ev := range evs {
		if ev.Kind == event.SessionIdle {
			continue
		}
		feed <- ev
	}
	mach := state.New(state.DefaultConfig())
	var out bytes.Buffer
	p := tea.NewProgram(
		ui.New(ui.Config{
			Machine: mach,
			Cfg:     config.Default(),
			Events:  feed,
			Static:  true, // a finished frame, not one caught mid-arrival
			// No injected clock: the frame-rate gate measures real elapsed time,
			// so a frozen one suppresses every repaint after the first. The
			// CONTENT clock still comes from the events themselves.
		}),
		tea.WithOutput(&out),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }() //nolint:errcheck // asserted through the frame below
	p.Send(tea.WindowSizeMsg{Width: 64, Height: 30})
	time.Sleep(600 * time.Millisecond)
	w := mach.Snapshot()
	p.Quit()
	<-done

	// What the pane KNOWS, from the world it built out of Codex's files…
	if len(w.Agents) != len(fanout.Threads)-1 {
		t.Fatalf("%d agents for %d subagent threads", len(w.Agents), len(fanout.Threads)-1)
	}
	if w.Main.Model == "" {
		t.Error("the session's model never reached the world")
	}
	// …and what it DREW. Bubbletea repaints by patching, so this is the whole
	// byte stream rather than a final screen.
	painted := out.String()
	for _, a := range w.Agents {
		if a.Name == "" || a.Model == "" || a.Tokens == 0 {
			t.Errorf("agent %s is missing name/model/tokens: %+v", a.ID, a)
		}
		if !strings.Contains(painted, strings.ReplaceAll(a.Name, " ", "-")) {
			t.Errorf("the pane never drew %q", a.Name)
		}
	}
	if !strings.Contains(painted, "gpt-6-astra") {
		t.Error("the pane never drew the model")
	}
	t.Logf("world: %d agents, main %s / %dk tokens", len(w.Agents), w.Main.Model, w.Main.Tokens/1000)
	if t.Failed() {
		t.Logf("painted %d bytes; contains RETURNED=%v current=%v",
			len(painted), strings.Contains(painted, "RETURNED"), strings.Contains(painted, "current"))
	}
}
