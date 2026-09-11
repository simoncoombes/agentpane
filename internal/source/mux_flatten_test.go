package source

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// TestMuxFlattensAtIngest is §13.1(b) at the boundary: whatever a source emits,
// what leaves the mux is one row wide — and the exact command is still there for
// `y`, because a python3 one-liner with its newlines turned into spaces is no
// longer something you can paste.
func TestMuxFlattensAtIngest(t *testing.T) {
	const cmd = "/usr/bin/python3 -c \"\nimport json\nprint(1)\n\"\t&& echo ok"

	src := &fixedSource{name: "hooks", evs: []event.Event{
		{Kind: event.ToolStart, AgentID: "a1", Tool: "Bash", Target: cmd,
			Says: "Run\na  one-liner", Detail: "started\nnow"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := Mux(ctx, src)
	if err != nil {
		t.Fatalf("Mux: %v", err)
	}
	var ev event.Event
	select {
	case ev = <-out:
	case <-time.After(time.Second):
		t.Fatal("no event reached the consumer")
	}

	if strings.ContainsAny(ev.Target, "\n\t") {
		t.Errorf("Target left the mux unflattened: %q", ev.Target)
	}
	if strings.ContainsAny(ev.Says, "\n\t") || strings.Contains(ev.Says, "  ") {
		t.Errorf("Says left the mux unflattened: %q", ev.Says)
	}
	if strings.ContainsAny(ev.Detail, "\n\t") {
		t.Errorf("Detail left the mux unflattened: %q", ev.Detail)
	}
	if got := ev.TargetRaw(); got != cmd {
		t.Errorf("the exact command did not survive: %q, want %q", got, cmd)
	}
	if ev.Seq == 0 {
		t.Error("flattening lost the ingest Seq")
	}
}

// Text that is already one line is passed through untouched, so no source pays
// for the guarantee and RawTarget stays empty when there is nothing to keep.
func TestMuxLeavesCleanTextAlone(t *testing.T) {
	src := &fixedSource{name: "transcript", evs: []event.Event{
		{Kind: event.ToolEnd, AgentID: "a1", Tool: "Bash", Target: "pnpm test --filter auth"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := Mux(ctx, src)
	if err != nil {
		t.Fatalf("Mux: %v", err)
	}
	ev := <-out
	if ev.Target != "pnpm test --filter auth" {
		t.Errorf("Target = %q, want it untouched", ev.Target)
	}
	if ev.RawTarget != "" {
		t.Errorf("RawTarget = %q, want empty when nothing was changed", ev.RawTarget)
	}
}

// fixedSource emits a fixed slice then holds the channel open until ctx dies.
type fixedSource struct {
	name string
	evs  []event.Event
}

func (s *fixedSource) Name() string { return s.name }

func (s *fixedSource) Events(ctx context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event, len(s.evs))
	for _, ev := range s.evs {
		ch <- ev
	}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
