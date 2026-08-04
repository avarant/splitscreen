package gateway

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/avarant/splitscreen/config"
	"github.com/avarant/splitscreen/internal/surface"
)

// stream coalesces a turn's output into periodic in-place edits.
//
// Consolidating every runner onto one chat app means one rate-limit bucket, so
// editing per token would throttle the whole fleet. The flush interval is a real
// control, not a cosmetic one.
type stream struct {
	gw   *Gateway
	turn *turnContext

	mu       sync.Mutex
	body     strings.Builder
	activity []string
	posted   bool
	ref      surface.Ref
	dirty    bool
	closed   bool

	stop chan struct{}
	done chan struct{}
}

func (g *Gateway) streamFor(turn *turnContext) *stream {
	if v, ok := g.streams.Load(turn.TurnID); ok {
		return v.(*stream)
	}
	s := &stream{
		gw:   g,
		turn: turn,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	actual, loaded := g.streams.LoadOrStore(turn.TurnID, s)
	if loaded {
		return actual.(*stream)
	}
	go s.loop(g.cfg.Load().Gateway.StreamInterval.Duration())
	return s
}

func (s *stream) loop(interval time.Duration) {
	defer close(s.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.flush(context.Background())
		}
	}
}

// AppendText adds assistant output.
func (s *stream) AppendText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	s.body.WriteString(text)
	s.dirty = true
	s.mu.Unlock()
}

// AppendActivity records a tool invocation. Tool lines are kept separate from
// assistant prose so the transcript stays readable when the two interleave, and
// so they can be dropped from the finished message without disturbing it.
func (s *stream) AppendActivity(line string) {
	if s.turn.Activity == config.ActivityHidden {
		return
	}
	s.mu.Lock()
	s.activity = append(s.activity, line)
	if len(s.activity) > 12 {
		// Keep the tail: what a long turn is doing now matters more than what it
		// started with, and the full record is in the audit log regardless.
		s.activity = s.activity[len(s.activity)-12:]
	}
	s.dirty = true
	s.mu.Unlock()
}

// render composes the message. On the final pass, activity lines are dropped
// unless the runner asked to keep them: they exist to show a long turn is alive,
// which stops being useful the moment the answer arrives.
func (s *stream) render(final bool) string {
	var b strings.Builder
	b.WriteString(s.body.String())
	showActivity := !final || s.turn.Activity == config.ActivityFull
	if len(s.activity) > 0 && showActivity {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for _, a := range s.activity {
			b.WriteString("\n_")
			b.WriteString(a)
			b.WriteString("_")
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *stream) flush(ctx context.Context) { s.flushWith(ctx, false) }

func (s *stream) flushWith(ctx context.Context, final bool) {
	s.mu.Lock()
	if (!s.dirty && !final) || s.closed {
		s.mu.Unlock()
		return
	}
	text := s.render(final)
	posted, ref := s.posted, s.ref
	s.dirty = false
	s.mu.Unlock()

	if text == "" {
		return
	}

	srf, ok := s.gw.surfaceFor(s.turn.Surface)
	if !ok {
		return
	}

	if !posted {
		newRef, err := srf.Post(ctx, surface.Post{
			Channel: s.turn.Channel,
			Thread:  s.turn.Thread,
			Text:    text,
			Persona: s.turn.Persona,
		})
		if err != nil {
			s.gw.log.Warn("stream post failed", "turn", s.turn.TurnID, "err", err)
			s.mu.Lock()
			s.dirty = true // retry on the next tick rather than losing output
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.posted, s.ref = true, newRef
		s.mu.Unlock()
		return
	}

	if err := srf.Update(ctx, ref, surface.Post{
		Channel: s.turn.Channel,
		Thread:  s.turn.Thread,
		Text:    text,
		Persona: s.turn.Persona,
	}); err != nil {
		s.gw.log.Warn("stream update failed", "turn", s.turn.TurnID, "err", err)
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
}

// Close performs a final flush and stops the ticker.
func (s *stream) Close(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = false // allow the final flush through
	s.mu.Unlock()

	close(s.stop)
	<-s.done
	// Final pass: strips the activity lines that were only ever there to show
	// the turn was alive.
	s.flushWith(ctx, true)

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	s.gw.streams.Delete(s.turn.TurnID)
}

// HasOutput reports whether anything was rendered, so a turn that produced
// nothing can say so rather than leaving silence.
func (s *stream) HasOutput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.Len() > 0 || len(s.activity) > 0
}
