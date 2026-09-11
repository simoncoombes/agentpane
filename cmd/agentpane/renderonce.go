package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simoncoombes/agentpane/internal/config"
	"github.com/simoncoombes/agentpane/internal/source/fake"
	"github.com/simoncoombes/agentpane/internal/state"
	"github.com/simoncoombes/agentpane/internal/ui"
)

// cmdRenderOnce implements `--render-once [--cols N --rows N]`: drive the
// fake source to the §2.10 snapshot through the real state machine — exactly
// like the ui snapshot tests — render one frame through the ui Model's own
// pure path, print it, exit. No tty needed: this is the CI/screenshot path.
func cmdRenderOnce(stdout io.Writer, fl cliFlags) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seek := fl.demoSeek
	if seek < 0 {
		seek = 0
	}
	src := fake.New(fake.Options{SeekSeconds: seek})
	ch, err := src.Events(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentpane: %v\n", err)
		return 1
	}

	machine := state.New(state.DefaultConfig())
drain:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break drain
			}
			machine.Apply(ev, ev.At)
		case <-time.After(500 * time.Millisecond):
			break drain // seeked burst consumed; the rest is real-time paced
		}
	}
	now := fake.Epoch().Add(time.Duration(seek) * time.Second)
	machine.Tick(now)

	cfg := config.Default()
	if fl.noColor || os.Getenv("NO_COLOR") != "" {
		cfg.Color = false
	}
	if fl.width == "wide" || fl.width == "narrow" {
		cfg.Width = fl.width
	}

	model := ui.New(ui.Config{
		Machine:      machine,
		Cfg:          cfg,
		AccentIndex:  2, // the goldens' accent; no tty to probe here
		AccentReason: "render-once: fixed accent 2 (no tty probed)",
		SourceName:   "fake",
		Version:      Version,
		Sessions:     demoSessionRows, // §3.8a: >1 session → header shows the id
		Static:       true,            // one frame: no opening draw-in
		Now:          func() time.Time { return now },
	})
	next, _ := model.Update(tea.WindowSizeMsg{Width: fl.cols, Height: fl.rows})
	m, ok := next.(*ui.Model)
	if !ok {
		fmt.Fprintln(os.Stderr, "agentpane: render-once: unexpected model type")
		return 1
	}
	fmt.Fprintln(stdout, m.View())
	return 0
}
