package claudecode

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/avarant/splitscreen/internal/harness"
)

// newParser wires a session's stdout parser to a scripted stream, with a
// trivially-exiting process standing in for the CLI.
func newParser(t *testing.T, lines string) *session {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &session{cmd: cmd, events: make(chan harness.Event, 64)}
	s.running.Store(true)
	go s.readStdout(io.NopCloser(strings.NewReader(lines)))
	return s
}

func collect(t *testing.T, s *session) []harness.Event {
	t.Helper()
	var out []harness.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("timed out collecting events")
		}
	}
}

func TestParsesTextAndToolUse(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-abc"}`,
		`{"type":"assistant","session_id":"sess-abc","message":{"model":"claude-opus-5","content":[{"type":"text","text":"Looking now."}]}}`,
		`{"type":"assistant","session_id":"sess-abc","message":{"content":[{"type":"tool_use","id":"call1","name":"Bash","input":{"command":"ls -la"}}]}}`,
		`{"type":"result","session_id":"sess-abc","usage":{"input_tokens":120,"output_tokens":45,"cache_creation_input_tokens":30,"cache_read_input_tokens":9000},"total_cost_usd":0.0123}`,
	}, "\n")

	s := newParser(t, stream)
	events := collect(t, s)

	var sawSession, sawText, sawTool, sawUsage, sawDone bool
	for _, ev := range events {
		switch ev.Kind {
		case harness.EventSession:
			sawSession = ev.SessionID == "sess-abc"
		case harness.EventText:
			sawText = ev.Text == "Looking now."
		case harness.EventToolUse:
			sawTool = ev.Tool == "Bash" && ev.CallID == "call1" && ev.Summary == "ls -la"
		case harness.EventUsage:
			sawUsage = true
			u := ev.Usage
			if !u.Known {
				t.Error("usage should be marked known when counters are present")
			}
			// The four counters must survive as four counters.
			if u.InputTokens != 120 || u.OutputTokens != 45 ||
				u.CacheWriteTokens != 30 || u.CacheReadTokens != 9000 {
				t.Errorf("counters = %+v", u)
			}
			if u.Model != "claude-opus-5" {
				t.Errorf("model = %q; it should carry over from the assistant message", u.Model)
			}
			if u.ProviderCostUSD == nil || *u.ProviderCostUSD != 0.0123 {
				t.Errorf("provider cost = %v; kept only as a cross-check", u.ProviderCostUSD)
			}
		case harness.EventDone:
			sawDone = ev.SessionID == "sess-abc" && ev.ToolCalls == 1
		}
	}
	if !sawSession || !sawText || !sawTool || !sawUsage || !sawDone {
		t.Fatalf("missing events (session=%v text=%v tool=%v usage=%v done=%v): %+v",
			sawSession, sawText, sawTool, sawUsage, sawDone, events)
	}
}

// A result with no usage block must report usage as unknown, not as zeros.
func TestMissingUsageIsUnknown(t *testing.T) {
	s := newParser(t, `{"type":"result","session_id":"s1"}`)
	for _, ev := range collect(t, s) {
		if ev.Kind == harness.EventUsage {
			if ev.Usage.Known {
				t.Fatal("usage was marked known without any counters")
			}
			if ev.Usage.InputTokens != 0 {
				t.Fatalf("counters = %+v", ev.Usage)
			}
			return
		}
	}
	t.Fatal("no usage event was emitted")
}

func TestErrorResult(t *testing.T) {
	s := newParser(t,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"tool failed"}`)
	for _, ev := range collect(t, s) {
		if ev.Kind == harness.EventError {
			if !strings.Contains(ev.Error, "tool failed") {
				t.Fatalf("error = %q", ev.Error)
			}
			return
		}
	}
	t.Fatal("no error event was emitted")
}

func TestNonJSONNoiseIsIgnored(t *testing.T) {
	s := newParser(t, strings.Join([]string{
		`some warning printed to stdout`,
		``,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"still here"}]}}`,
	}, "\n"))
	for _, ev := range collect(t, s) {
		if ev.Kind == harness.EventText && ev.Text == "still here" {
			return
		}
	}
	t.Fatal("noise on stdout stopped parsing")
}

func TestAdapterIsRegistered(t *testing.T) {
	a, err := harness.Get("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if a.PermissionToolName() == "" {
		t.Error("the adapter must name a permission tool; without it the harness would decide for itself")
	}
	if a.DefaultCredentialEnv() == "" {
		t.Error("the adapter must name a credential variable")
	}
}
