package slackx

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/avarant/splitscreen/internal/surface"
	"github.com/avarant/splitscreen/protocol"
)

type recordingHandler struct {
	mu        sync.Mutex
	messages  []surface.Inbound
	decisions []surface.Decision
}

func (h *recordingHandler) OnMessage(_ context.Context, in surface.Inbound) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, in)
}

func (h *recordingHandler) OnDecision(_ context.Context, d surface.Decision) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.decisions = append(h.decisions, d)
}

func event(msg *slackevents.MessageEvent) slackevents.EventsAPIEvent {
	return slackevents.EventsAPIEvent{
		Type:       slackevents.CallbackEvent,
		InnerEvent: slackevents.EventsAPIInnerEvent{Data: msg},
	}
}

func TestMessageNormalization(t *testing.T) {
	s := &Surface{selfID: "UBOT"}
	h := &recordingHandler{}

	s.handleEvent(context.Background(), h, event(&slackevents.MessageEvent{
		User: "U1", Text: "hello", Channel: "C1",
		TimeStamp: "1700.1", ChannelType: "channel",
	}))

	if len(h.messages) != 1 {
		t.Fatalf("messages = %d", len(h.messages))
	}
	in := h.messages[0]
	if in.Text != "hello" || in.User.ID != "U1" || in.Channel != "C1" {
		t.Fatalf("inbound = %+v", in)
	}
	// A top-level message opens a thread keyed on its own timestamp, so replies
	// and the original share one conversation.
	if in.Thread != "1700.1" {
		t.Errorf("thread = %q, want the message timestamp", in.Thread)
	}
	if in.IsDM {
		t.Error("a channel message was flagged as a DM")
	}
}

func TestThreadedReplyKeepsTheParentThread(t *testing.T) {
	s := &Surface{selfID: "UBOT"}
	h := &recordingHandler{}
	s.handleEvent(context.Background(), h, event(&slackevents.MessageEvent{
		User: "U1", Text: "reply", Channel: "C1",
		TimeStamp: "1700.9", ThreadTimeStamp: "1700.1",
	}))
	if h.messages[0].Thread != "1700.1" {
		t.Fatalf("thread = %q, want the parent", h.messages[0].Thread)
	}
}

func TestDMFlagged(t *testing.T) {
	s := &Surface{selfID: "UBOT"}
	h := &recordingHandler{}
	s.handleEvent(context.Background(), h, event(&slackevents.MessageEvent{
		User: "U1", Text: "hi", Channel: "D1", TimeStamp: "1", ChannelType: "im",
	}))
	if !h.messages[0].IsDM {
		t.Fatal("a DM was not flagged; it would miss the dm route")
	}
}

// Our own posts and every edit/delete/join subtype must be ignored, or the bot
// would answer itself.
func TestIgnoredEvents(t *testing.T) {
	s := &Surface{selfID: "UBOT"}
	cases := []struct {
		name string
		ev   *slackevents.MessageEvent
	}{
		{"our own message", &slackevents.MessageEvent{User: "UBOT", Text: "x", TimeStamp: "1"}},
		{"another bot", &slackevents.MessageEvent{BotID: "B1", User: "U1", Text: "x", TimeStamp: "1"}},
		{"no user", &slackevents.MessageEvent{Text: "x", TimeStamp: "1"}},
		{"an edit", &slackevents.MessageEvent{User: "U1", Text: "x", TimeStamp: "1", SubType: "message_changed"}},
		{"a join", &slackevents.MessageEvent{User: "U1", Text: "x", TimeStamp: "1", SubType: "channel_join"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &recordingHandler{}
			s.handleEvent(context.Background(), h, event(tc.ev))
			if len(h.messages) != 0 {
				t.Fatalf("event was handled: %+v", h.messages)
			}
		})
	}
}

// file_share is how a message carrying an attachment arrives. Dropping it as
// "just another subtype" would silently discard every upload.
func TestFileShareIsNotDropped(t *testing.T) {
	s := &Surface{selfID: "UBOT"}
	h := &recordingHandler{}
	s.handleEvent(context.Background(), h, event(&slackevents.MessageEvent{
		User: "U1", Channel: "C1", TimeStamp: "1", SubType: "file_share",
		Message: &slack.Msg{
			Text: "here is the data",
			Files: []slack.File{{
				Name: "data.csv", Mimetype: "text/csv", Size: 42,
				URLPrivateDownload: "https://files.slack.com/x",
			}},
		},
	}))

	if len(h.messages) != 1 {
		t.Fatalf("a file_share message was dropped")
	}
	in := h.messages[0]
	if len(in.Files) != 1 {
		t.Fatalf("files = %d", len(in.Files))
	}
	f := in.Files[0]
	if f.Name != "data.csv" || f.Mime != "text/csv" || f.Size != 42 {
		t.Fatalf("file = %+v", f)
	}
	if f.Open == nil {
		t.Error("the file has no opener; the gateway could not fetch it")
	}
	if in.Text != "here is the data" {
		t.Errorf("text = %q; the caption lives on the nested message", in.Text)
	}
}

func TestDecisionParsedFromButton(t *testing.T) {
	s := &Surface{}
	h := &recordingHandler{}

	s.handleInteraction(context.Background(), h, slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		User: slack.User{ID: "U9", Name: "alice"},
		Channel: slack.Channel{GroupConversation: slack.GroupConversation{
			Conversation: slack.Conversation{ID: "C1"},
		}},
		Message: slack.Message{Msg: slack.Msg{Timestamp: "p1", ThreadTimestamp: "T1"}},
		ActionCallback: slack.ActionCallbacks{
			BlockActions: []*slack.BlockAction{{
				ActionID: permissionAction + "_allow",
				Value:    "req-123|allow",
			}},
		},
	})

	if len(h.decisions) != 1 {
		t.Fatalf("decisions = %d", len(h.decisions))
	}
	d := h.decisions[0]
	// The request id rides in the button value, so resolving a decision needs no
	// server-side lookup table that a gateway restart would lose.
	if d.RequestID != "req-123" || d.Decision != protocol.DecisionAllow {
		t.Fatalf("decision = %+v", d)
	}
	if d.User.ID != "U9" || d.User.Display != "alice" {
		t.Fatalf("user = %+v", d.User)
	}
	if d.Ref.ID != "p1" || d.Ref.Channel != "C1" {
		t.Fatalf("ref = %+v", d.Ref)
	}
}

func TestUnrelatedInteractionIgnored(t *testing.T) {
	s := &Surface{}
	h := &recordingHandler{}
	s.handleInteraction(context.Background(), h, slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		ActionCallback: slack.ActionCallbacks{
			BlockActions: []*slack.BlockAction{{ActionID: "someone_elses_button", Value: "x|y"}},
		},
	})
	if len(h.decisions) != 0 {
		t.Fatal("an unrelated button was treated as a permission decision")
	}
}

func TestButtonEncodesRequestAndDecision(t *testing.T) {
	b := button("Deny", string(protocol.DecisionDeny), "req-7", slack.StyleDanger)
	if b.Value != "req-7|deny" {
		t.Fatalf("value = %q", b.Value)
	}
	if !strings.HasPrefix(b.ActionID, permissionAction) {
		t.Fatalf("action id = %q; the handler filters on this prefix", b.ActionID)
	}
}

func TestNewRejectsMisplacedTokens(t *testing.T) {
	// Swapping the two tokens is an easy mistake with a confusing failure mode
	// much later, at connect time.
	if _, err := New("xapp-1", "xoxb-1"); err == nil {
		t.Fatal("swapped tokens were accepted")
	}
	if _, err := New("xoxb-1", "xapp-1"); err != nil {
		t.Fatalf("valid tokens rejected: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("x", 200)
	got := truncate(long, 50)
	if len(got) <= 50 || !strings.Contains(got, "truncated") {
		t.Errorf("got %d chars, %q", len(got), got[len(got)-20:])
	}
}
