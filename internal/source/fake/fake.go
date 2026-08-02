// Package fake ships the deterministic demo Source: a scripted replay of the
// SPEC §2.10 scenario, driven by `agentpane --demo` (SPEC §1.4). All event
// content is derived from a fixed epoch — never from the wall clock — so any
// two replays are identical. Wall-clock time is consulted only for pacing,
// through an injectable timer.
package fake

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/simoncoombes/agentpane/internal/event"
)

// Options configures playback. The zero value plays the full script from t=0
// at real-time speed.
type Options struct {
	Speed       float64 // wall-clock multiplier; <=0 or NaN means 1.0, +Inf means no pacing
	SeekSeconds int     // events at t <= SeekSeconds are emitted instantly on start
	Paused      bool    // start paused; Step/Resume release events
}

// Source is a deterministic event.Source replaying the §2.10 timeline.
// Pause, Step, and Resume are safe to call from any goroutine at any time,
// which is what the UI's demo keybindings need.
type Source struct {
	opts Options

	mu      sync.Mutex
	cond    *sync.Cond
	paused  bool
	steps   int
	started bool

	// after paces playback; replaced in tests with a virtual clock so no
	// logic path ever depends on real time.
	after func(time.Duration) <-chan time.Time
}

// New returns a Source configured by opts. Malformed options are tolerated,
// never fatal (C9): a non-positive or NaN speed plays at 1.0, a negative
// seek is a no-op seek.
func New(opts Options) *Source {
	s := &Source{opts: opts, paused: opts.Paused, after: time.After}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Name implements event.Source.
func (s *Source) Name() string { return "fake" }

// Events starts playback and implements event.Source. A Source plays once;
// a second call returns an error rather than a second competing playback.
func (s *Source) Events(ctx context.Context) (<-chan event.Event, error) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("fake: Events already started")
	}
	s.started = true
	s.mu.Unlock()

	ch := make(chan event.Event)
	go s.run(ctx, ch)
	return ch, nil
}

// Pause holds playback before the next event is emitted. Idempotent.
func (s *Source) Pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

// Resume releases playback and discards any queued steps. Idempotent.
func (s *Source) Resume() {
	s.mu.Lock()
	s.paused = false
	s.steps = 0
	s.mu.Unlock()
	s.cond.Broadcast()
}

// Step emits exactly one event when paused. When running it pauses instead,
// so a single demo keybinding degrades sanely from either state.
func (s *Source) Step() {
	s.mu.Lock()
	if s.paused {
		s.steps++
	} else {
		s.paused = true
	}
	s.mu.Unlock()
	s.cond.Broadcast()
}

func (s *Source) run(ctx context.Context, ch chan<- event.Event) {
	defer close(ch)
	// Wake a gate waiter when ctx dies. Broadcasting under mu orders the
	// wakeup against gate's check-then-Wait, closing the missed-wakeup
	// window.
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cond.Broadcast()
	})
	defer stop()

	seekEnd := epoch.Add(time.Duration(s.opts.SeekSeconds) * time.Second)
	cursor := epoch
	for _, ev := range script() {
		if ev.At.After(seekEnd) {
			if cursor.Before(seekEnd) {
				cursor = seekEnd
			}
			if d := s.pace(ev.At.Sub(cursor)); d > 0 {
				select {
				case <-s.after(d):
				case <-ctx.Done():
					return
				}
			}
			cursor = ev.At
		}
		if !s.gate(ctx) {
			return
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}
	// Script exhausted: hold the state open for the UI; close only on ctx.
	<-ctx.Done()
}

// gate blocks while paused with no queued step. Reports false once ctx dies.
func (s *Source) gate(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.paused && s.steps == 0 && ctx.Err() == nil {
		s.cond.Wait()
	}
	if ctx.Err() != nil {
		return false
	}
	if s.steps > 0 {
		s.steps--
	}
	return true
}

func (s *Source) pace(d time.Duration) time.Duration {
	sp := s.opts.Speed
	if math.IsInf(sp, 1) {
		return 0
	}
	if sp <= 0 || math.IsNaN(sp) {
		sp = 1
	}
	return time.Duration(float64(d) / sp)
}

var _ event.Source = (*Source)(nil)
