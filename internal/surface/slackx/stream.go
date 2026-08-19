package slackx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/avarant/splitscreen/internal/surface"
)

// Slack's streaming methods render a message as it is written: prose types in,
// and task_update chunks become task cards that carry their own status. The
// card timeline is collapsed by default, so a finished turn shows the answer
// with the steps folded behind a disclosure the reader opens if they care.
//
// This is why the gateway no longer needs an activity cap or a transient mode:
// both existed to keep a rewritten message short.

// errStreamUnsupported is returned once the workspace has told us streaming is
// not available to this app, so the gateway stops asking.
var errStreamUnsupported = errors.New("slack: streaming not available for this app")

// streamDenied are the API errors that mean "never going to work", as opposed
// to a transient failure worth retrying on the next turn. Anything else leaves
// streaming enabled: a network blip must not permanently downgrade the fleet.
var streamDenied = []string{
	"missing_scope",
	"not_allowed_token_type",
	"method_not_supported_for_channel_type",
	"invalid_arguments",
	"unknown_method",
}

func deniedPermanently(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range streamDenied {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// OpenStream starts a native streaming message.
//
// Slack requires the stream to be a reply, and requires a recipient when the
// stream lands in a channel rather than a DM. Both are already known: every
// splitscreen turn is threaded, and the turn carries the user who asked.
func (s *Surface) OpenStream(ctx context.Context, p surface.Post) (surface.Stream, error) {
	if s.noStream.Load() {
		return nil, errStreamUnsupported
	}
	if p.Thread == "" {
		return nil, errors.New("slack: streaming requires a thread")
	}

	opts := []slack.MsgOption{
		slack.MsgOptionTS(p.Thread),
		slack.MsgOptionTaskDisplayMode(slack.TaskDisplayModeTimeline),
	}
	if p.User != "" {
		opts = append(opts, slack.MsgOptionRecipientUserID(p.User))
	}
	if teamID := s.teamID; teamID != "" {
		opts = append(opts, slack.MsgOptionRecipientTeamID(teamID))
	}
	opts = append(opts, personaOptions(p.Persona)...)

	channel, ts, err := s.api.StartStreamContext(ctx, p.Channel, opts...)
	if err != nil {
		if deniedPermanently(err) {
			// Latched and logged once by the caller: every later turn takes the
			// post-and-edit path without a wasted round trip.
			s.noStream.Store(true)
		}
		return nil, fmt.Errorf("slack: start stream: %w", err)
	}
	return &stream{
		srf:     s,
		ref:     surface.Ref{Channel: channel, Thread: p.Thread, ID: ts},
		persona: p.Persona,
	}, nil
}

type stream struct {
	srf     *Surface
	ref     surface.Ref
	persona surface.Persona

	mu     sync.Mutex
	closed bool
}

func (st *stream) Ref() surface.Ref { return st.ref }

func (st *stream) Append(ctx context.Context, u surface.StreamUpdate) error {
	chunks := chunksFor(u)
	if len(chunks) == 0 {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return errors.New("slack: stream already closed")
	}
	_, _, err := st.srf.api.AppendStreamContext(ctx, st.ref.Channel, st.ref.ID,
		slack.MsgOptionChunks(chunks...))
	if err != nil {
		return fmt.Errorf("slack: append stream: %w", err)
	}
	return nil
}

func (st *stream) Close(ctx context.Context, u surface.StreamUpdate) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return nil
	}
	st.closed = true

	opts := []slack.MsgOption{}
	if chunks := chunksFor(u); len(chunks) > 0 {
		opts = append(opts, slack.MsgOptionChunks(chunks...))
	}
	_, _, err := st.srf.api.StopStreamContext(ctx, st.ref.Channel, st.ref.ID, opts...)
	if err != nil {
		return fmt.Errorf("slack: stop stream: %w", err)
	}
	return nil
}

// chunksFor converts one update into the streaming protocol's chunks.
//
// Text goes first: the prose that explains a step reads better above it, and
// that is the order the harness produced them in.
func chunksFor(u surface.StreamUpdate) []slack.StreamChunk {
	var chunks []slack.StreamChunk
	// The streaming API takes markdown, not Slack's mrkdwn dialect, so the
	// CommonMark the harness emits goes through untouched — unlike the
	// post-and-edit path, which has to convert.
	if text := u.Text; text != "" {
		chunks = append(chunks, slack.NewMarkdownTextChunk(text))
	}
	for _, step := range u.Steps {
		chunk := slack.NewTaskUpdateChunk(step.ID, truncateTitle(step.Title))
		chunk.Status = taskStatus(step.Status)
		chunk.Details = detailFor(step)
		if step.Output != "" {
			chunk.Output = truncate(step.Output, 2000)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func taskStatus(s surface.StepStatus) slack.TaskCardStatus {
	switch s {
	case surface.StepDone:
		return slack.TaskCardStatusComplete
	case surface.StepFailed:
		return slack.TaskCardStatusError
	default:
		return slack.TaskCardStatusInProgress
	}
}

// detailFor puts the arguments and the elapsed time under the title. Duration
// only appears once the step has finished, because a running step's elapsed
// time would be frozen at whatever it was when the chunk was sent.
func detailFor(step surface.Step) string {
	detail := step.Detail
	if step.Status != surface.StepRunning && step.Elapsed >= 100*time.Millisecond {
		took := step.Elapsed.Round(100 * time.Millisecond).String()
		if detail == "" {
			detail = took
		} else {
			detail += "  ·  " + took
		}
	}
	return truncate(detail, 2000)
}

// truncateTitle keeps a card title inside Slack's 256-character limit.
func truncateTitle(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if s == "" {
		return "tool"
	}
	return truncate(s, 250)
}
