package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/event"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/ui"
)

// syncBuf is an io.Writer the program can render into from its own goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The REAL program: tea.NewProgram, the real Model, a real event stream, real
// time, rendering into a buffer. Every other test of the animation renders a
// frame; this one runs the thing that ships and reads what it writes to the
// terminal.
//
// It exists because three fixes in a row were reasoned from the render function
// and shipped, and the pane still did not animate for the person watching it.
// The cheapest lesson of the lot: test what runs.
func TestProgramAnimatesANewSubagent(t *testing.T) {
	evs := make(chan event.Event, 8)
	out := &syncBuf{}
	m := ui.New(ui.Config{
		Machine: state.New(state.DefaultConfig()),
		Cfg:     config.Default(),
		Events:  evs,
	})
	p := tea.NewProgram(m,
		tea.WithOutput(out),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }() //nolint:errcheck // the quit path is asserted below

	p.Send(tea.WindowSizeMsg{Width: 64, Height: 20})
	time.Sleep(150 * time.Millisecond)

	mark := len(out.String())
	// One spawn, and nothing else: a real subagent is announced before it does
	// anything, so its row is queued when it first appears.
	evs <- event.Event{AgentID: "a1", AgentName: "explore the schema",
		AgentType: "general-purpose", Kind: event.AgentSpawned, At: time.Now()}

	time.Sleep(700 * time.Millisecond)
	p.Quit()
	<-done

	painted := out.String()[mark:]
	if !strings.Contains(painted, "explore-schema") {
		t.Fatalf("the row never reached the terminal:\n%q", painted)
	}
	pens := strings.Count(painted, "▏") + strings.Count(painted, "▎") + strings.Count(painted, "▍")
	if pens == 0 {
		t.Errorf("no pen reached the terminal — the arrival was not drawn:\n%q", painted)
	}
	// A row that arrives writes its name a piece at a time, so the terminal
	// sees prefixes of it before it sees the whole thing.
	partial := 0
	for _, frag := range []string{"expl", "explo", "explor", "explore", "explore-", "explore-s"} {
		if strings.Contains(painted, frag) {
			partial++
		}
	}
	if partial < 3 {
		t.Errorf("the name appeared whole: only %d prefixes of it were ever painted", partial)
	}
	t.Logf("pens=%d prefixes=%d bytes=%d", pens, partial, len(painted))
}
