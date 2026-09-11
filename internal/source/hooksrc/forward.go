package hooksrc

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"time"
)

const (
	// A hook payload beyond this is not something we forward; the TUI's
	// other channels (transcript tail) carry oversized tool output.
	forwardMaxPayload = 8 * 1024 * 1024

	forwardDialTimeout  = 100 * time.Millisecond
	forwardWriteTimeout = 250 * time.Millisecond
)

// Forward implements the `agentpane hook` side of SPEC §1.4: read ONE hook
// JSON document from stdin, extract session_id, connect to the per-session
// socket, write the payload as a single line, close.
//
// It must never fail loudly: hooks capture stdout/stderr and a nonzero exit
// can block the tool (DATA-SOURCES §3.0), so every failure path — absent
// socket, junk JSON, short read — returns nil so the caller exits 0
// silently. Nothing here writes to stdout or stderr.
func Forward(stdin io.Reader, socketPath func(sessionID string) string) (err error) {
	defer func() {
		// Belt and braces for C9: a panic must not become a hook failure.
		if recover() != nil {
			err = nil
		}
	}()

	if stdin == nil || socketPath == nil {
		return nil
	}
	data, rerr := io.ReadAll(io.LimitReader(stdin, forwardMaxPayload))
	if rerr != nil || len(data) == 0 {
		return nil
	}

	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &p) != nil || p.SessionID == "" {
		return nil
	}

	// The transport is newline-delimited; compact to guarantee one line
	// even if the payload arrives pretty-printed.
	var line bytes.Buffer
	if json.Compact(&line, data) != nil {
		return nil
	}
	line.WriteByte('\n')

	conn, derr := net.DialTimeout("unix", socketPath(p.SessionID), forwardDialTimeout)
	if derr != nil {
		return nil // no TUI listening — the normal case, stay silent
	}
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(forwardWriteTimeout))
	conn.Write(line.Bytes())
	return nil
}
