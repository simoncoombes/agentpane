// Package hooksrc implements the HookSource half of SPEC.md §1.4: a unix
// socket listener that receives newline-delimited Claude Code hook payloads
// (forwarded by the `agentpane hook` subcommand, see forward.go) and maps
// them onto the frozen event taxonomy per docs/DATA-SOURCES.md §3 and §6.
package hooksrc

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

const (
	// Hook payloads observed are a few KB; tool_response can be large.
	scanInitialBuf = 64 * 1024
	scanMaxBuf     = 4 * 1024 * 1024

	// Each forwarder connection writes one line and closes; a peer that
	// stalls longer than this is abandoned so shutdown cannot hang on it.
	connReadTimeout = 10 * time.Second

	eventBufferSize = 256
)

// SocketPath returns the per-session socket path the forwarder and listener
// agree on: os.TempDir()/agentpane-<session>.sock (SPEC §1.4).
func SocketPath(sessionID string) string {
	return filepath.Join(os.TempDir(), "agentpane-"+sanitizeSession(sessionID)+".sock")
}

// sanitizeSession keeps the socket name inside TempDir even if a payload
// carries a hostile or malformed session_id (C9: never trust input).
func sanitizeSession(sessionID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		}
		return '-'
	}, sessionID)
}

// Source listens on a unix socket for forwarded hook payloads and implements
// event.Source. Transport failure emits SourceDisconnected and retries with
// backoff; recovery emits SourceConnected. The channel is closed only when
// the context ends.
type Source struct {
	socketPath string

	// mapper holds the payload→event mapping and its spawn→start correlation
	// state. One per Source: the queue it keeps is per session.
	mapper *Mapper

	// clock stamps Event.At at ingest (hook payloads carry no timestamp,
	// DATA-SOURCES §3.2). Injectable for tests.
	clock func() time.Time

	minBackoff time.Duration
	maxBackoff time.Duration

	skipped atomic.Uint64
}

func New(socketPath string) *Source {
	return &Source{
		socketPath: socketPath,
		mapper:     NewMapper(),
		clock:      time.Now,
		minBackoff: 100 * time.Millisecond,
		maxBackoff: 3 * time.Second,
	}
}

func (s *Source) Name() string { return "hooks" }

// Skipped reports how many malformed (junk JSON, torn, or unattributable)
// lines have been dropped since start.
func (s *Source) Skipped() uint64 { return s.skipped.Load() }

func (s *Source) Events(ctx context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event, eventBufferSize)
	go s.loop(ctx, ch)
	return ch, nil
}

func (s *Source) loop(ctx context.Context, ch chan event.Event) {
	defer close(ch)
	em := &emitter{ch: ch, clock: s.clock}
	backoff := s.minBackoff
	down := false

	for ctx.Err() == nil {
		ln, err := listenUnix(s.socketPath)
		if err != nil {
			if !down {
				em.emit(ctx, event.Event{Kind: event.SourceDisconnected, Detail: err.Error()})
				down = true
			}
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, s.maxBackoff)
			continue
		}

		em.emit(ctx, event.Event{Kind: event.SourceConnected})
		down = false
		backoff = s.minBackoff

		serveErr := s.serve(ctx, ln, em)
		ln.Close()
		if ctx.Err() != nil {
			// Cancelled: a successor listener (session re-attach) may have
			// already bound a fresh socket at this path — removing it here
			// would silently unlink the replacement. Leave cleanup to the
			// next listenUnix.
			return
		}
		os.Remove(s.socketPath)
		detail := ""
		if serveErr != nil {
			detail = serveErr.Error()
		}
		em.emit(ctx, event.Event{Kind: event.SourceDisconnected, Detail: detail})
		down = true
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// listenUnix removes a stale socket file before binding. The TUI owns the
// per-session socket, so an existing file is a leftover, not a peer.
func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		// Manual unlink discipline: Close() must never unlink by path — on
		// a session re-attach a successor listener may already own the path,
		// and the automatic unlink would remove ITS socket. The loop removes
		// the file explicitly on non-cancel teardown instead.
		ul.SetUnlinkOnClose(false)
	}
	return ln, nil
}

func (s *Source) serve(ctx context.Context, ln net.Listener, em *emitter) error {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		ln.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, conn, em)
		}()
	}
}

func (s *Source) handleConn(ctx context.Context, conn net.Conn, em *emitter) {
	defer conn.Close()
	// Wall clock on purpose: kernel deadlines compare against real time,
	// so the injectable event clock must not feed them.
	conn.SetReadDeadline(time.Now().Add(connReadTimeout))

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, scanInitialBuf), scanMaxBuf)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		evs, err := s.mapper.Map(line)
		if err != nil {
			s.skipped.Add(1)
			continue
		}
		em.emit(ctx, evs...)
	}
	// A scanner error here is a torn line or an oversized/aborted write:
	// count it and move on (C9 — never crash on malformed input). A final
	// unterminated fragment was already handed to the mapper above and either
	// parsed (a missing trailing newline is harmless) or counted there.
	if sc.Err() != nil {
		s.skipped.Add(1)
	}
}

// emitter serializes Seq assignment and channel sends so Seq is strictly
// monotonic in emission order even with concurrent hook connections.
type emitter struct {
	mu    sync.Mutex
	seq   uint64
	ch    chan<- event.Event
	clock func() time.Time
}

func (e *emitter) emit(ctx context.Context, evs ...event.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range evs {
		e.seq++
		ev.Seq = e.seq
		if ev.At.IsZero() {
			ev.At = e.clock()
		}
		select {
		case e.ch <- ev:
		case <-ctx.Done():
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
