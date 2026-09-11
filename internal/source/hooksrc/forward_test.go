package hooksrc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

func staticPath(p string) func(string) string {
	return func(string) string { return p }
}

// The forwarder runs inside a hook: any failure must be silent (return nil,
// exit 0), or it would block the user's tool calls (DATA-SOURCES §3.0).
func TestForwardNeverFails(t *testing.T) {
	missing := shortSocket(t) // path exists in no filesystem state

	tests := []struct {
		name       string
		stdin      string
		socketPath func(string) string
	}{
		{"missing socket", capUserPromptSubmit, staticPath(missing)},
		{"junk stdin", "not json at all", staticPath(missing)},
		{"empty stdin", "", staticPath(missing)},
		{"JSON without session_id", `{"hook_event_name":"Stop"}`, staticPath(missing)},
		{"nil socketPath func", capUserPromptSubmit, nil},
		{"socket path in missing dir", capUserPromptSubmit, staticPath("/nonexistent-dir-xyz/s.sock")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Forward(strings.NewReader(tt.stdin), tt.socketPath); err != nil {
				t.Errorf("Forward = %v, want nil", err)
			}
		})
	}

	if err := Forward(nil, staticPath(missing)); err != nil {
		t.Errorf("Forward(nil reader) = %v, want nil", err)
	}
}

func TestForwardDeliversOneLine(t *testing.T) {
	socket := shortSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type result struct {
		line string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		got <- result{line: line, err: err}
	}()

	var sawSession string
	sp := func(sessionID string) string {
		sawSession = sessionID
		return socket
	}
	if err := Forward(strings.NewReader(capUserPromptSubmit), sp); err != nil {
		t.Fatalf("Forward = %v, want nil", err)
	}
	if sawSession != "292ef2e2-..." {
		t.Errorf("extracted session_id = %q, want %q", sawSession, "292ef2e2-...")
	}

	r := <-got
	if r.err != nil {
		t.Fatalf("listener read: %v", r.err)
	}
	if !strings.HasSuffix(r.line, "\n") {
		t.Error("payload not newline-terminated")
	}
	if strings.TrimSuffix(r.line, "\n") != capUserPromptSubmit {
		t.Errorf("payload altered in transit:\n got: %q\nwant: %q", r.line, capUserPromptSubmit)
	}
}

// A pretty-printed payload must arrive as a single line: the transport is
// newline-delimited.
func TestForwardCompactsMultilineJSON(t *testing.T) {
	socket := shortSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
	}()

	pretty := "{\n  \"session_id\": \"s1\",\n  \"hook_event_name\": \"SessionStart\",\n  \"source\": \"startup\"\n}\n"
	if err := Forward(strings.NewReader(pretty), staticPath(socket)); err != nil {
		t.Fatalf("Forward = %v", err)
	}

	line := <-got
	body := strings.TrimSuffix(line, "\n")
	if strings.Contains(body, "\n") {
		t.Errorf("payload not compacted to one line: %q", line)
	}
	var p struct {
		SessionID string `json:"session_id"`
		HookEvent string `json:"hook_event_name"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("delivered payload not valid JSON: %v", err)
	}
	if p.SessionID != "s1" || p.HookEvent != "SessionStart" {
		t.Errorf("payload fields lost: %+v", p)
	}
}

// End to end: hook payload through Forward into a running Source.
func TestForwardIntoSource(t *testing.T) {
	socket := shortSocket(t)
	src := newTestSource(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, event.SourceConnected)

	// Wait for the listener before forwarding, as a real TUI would be up.
	c := dial(t, socket)
	c.Close()

	if err := Forward(strings.NewReader(capPermissionRequestMain), staticPath(socket)); err != nil {
		t.Fatalf("Forward = %v", err)
	}
	ev := recvKind(t, ch, event.PermissionRequested)
	if ev.Ask == nil || ev.Ask.Command != "touch /private/tmp/permreq-probe-a" {
		t.Errorf("Ask not preserved end to end: %+v", ev.Ask)
	}
}
