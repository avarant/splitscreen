package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fakeHandler struct {
	calls    []string
	failNext bool
}

func (f *fakeHandler) Tools(context.Context) ([]Tool, error) {
	return []Tool{{
		Name:        "do_thing",
		Description: "does a thing",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}, nil
}

func (f *fakeHandler) Call(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	if f.failNext {
		return nil, os.ErrPermission
	}
	return TextResult("did " + name), nil
}

// serve runs the loop over scripted input and returns the replies.
func serve(t *testing.T, h Handler, lines ...string) []map[string]any {
	t.Helper()

	var in strings.Builder
	for _, l := range lines {
		in.WriteString(l)
		in.WriteString("\n")
	}
	var out bytes.Buffer

	if err := Serve(context.Background(), "test", h, strings.NewReader(in.String()), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var results []map[string]any
	dec := json.NewDecoder(&out)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		results = append(results, m)
	}
	return results
}

func TestInitializeAndList(t *testing.T) {
	out := serve(t, &fakeHandler{},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(out) != 2 {
		t.Fatalf("got %d replies, want 2 (a notification must not be answered)", len(out))
	}

	init := out[0]["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v", init["protocolVersion"])
	}

	list := out[1]["result"].(map[string]any)
	tools := list["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	if tools[0].(map[string]any)["name"] != "do_thing" {
		t.Errorf("tool = %v", tools[0])
	}
}

func TestToolCall(t *testing.T) {
	h := &fakeHandler{}
	out := serve(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"do_thing","arguments":{"a":1}}}`,
	)
	if len(h.calls) != 1 || h.calls[0] != "do_thing" {
		t.Fatalf("calls = %v", h.calls)
	}
	result := out[0]["result"].(map[string]any)
	content := result["content"].([]any)
	if !strings.Contains(content[0].(map[string]any)["text"].(string), "did do_thing") {
		t.Fatalf("content = %v", content)
	}
}

// A tool error must come back as a result the model can see and react to, not
// as a transport error that aborts the turn.
func TestToolErrorIsAResult(t *testing.T) {
	out := serve(t, &fakeHandler{failNext: true},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"do_thing"}}`,
	)
	if _, isTransportError := out[0]["error"]; isTransportError {
		t.Fatal("a tool failure was reported as a transport error")
	}
	result := out[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("result = %v, want isError", result)
	}
}

func TestUnknownMethod(t *testing.T) {
	out := serve(t, &fakeHandler{}, `{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	e := out[0]["error"].(map[string]any)
	if e["code"].(float64) != -32601 {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestMalformedLineDoesNotKillTheLoop(t *testing.T) {
	out := serve(t, &fakeHandler{},
		`this is not json`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	)
	if len(out) == 0 {
		t.Fatal("the loop stopped after a malformed line")
	}
}

func TestWrapContentPassesThroughUpstreamResults(t *testing.T) {
	// An upstream MCP result already has a content envelope; wrapping it again
	// would bury the payload one level deeper than the harness expects.
	upstream := json.RawMessage(`{"content":[{"type":"text","text":"hi"}]}`)
	got := wrapContent(upstream)
	raw, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("wrapped an already-enveloped result: %T", got)
	}
	if string(raw) != string(upstream) {
		t.Fatalf("result = %s", raw)
	}

	bare := wrapContent(json.RawMessage(`{"value":42}`)).(map[string]any)
	if len(bare["content"].([]any)) != 1 {
		t.Fatalf("bare payload = %v", bare)
	}
}
