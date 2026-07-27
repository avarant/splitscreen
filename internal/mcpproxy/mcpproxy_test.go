package mcpproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallForwardsAndUnwraps(t *testing.T) {
	var gotUser, gotPass string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{
		Name: "jira", URL: srv.URL, Auth: AuthBasic,
		User: "bot@example.com", Secret: "tok",
	}})

	out, err := p.Call(context.Background(), "jira", "get_issue", json.RawMessage(`{"key":"PROJ-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("result = %s", out)
	}
	if gotUser != "bot@example.com" || gotPass != "tok" {
		t.Errorf("basic auth = %q/%q", gotUser, gotPass)
	}
	if gotBody["method"] != "tools/call" {
		t.Errorf("method = %v", gotBody["method"])
	}
	params, _ := gotBody["params"].(map[string]any)
	if params["name"] != "get_issue" {
		t.Errorf("tool name = %v", params["name"])
	}
}

// The credential stays here. A deny rule is enforced before the call leaves the
// gateway, so no agent decision can reach a forbidden tool.
func TestDenyRulesEnforcedBeforeTheCall(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{Name: "jira", URL: srv.URL, Deny: []string{"deleteIssue", "admin_*"}}})

	for _, tool := range []string{"deleteIssue", "admin_reset"} {
		if _, err := p.Call(context.Background(), "jira", tool, nil); err == nil {
			t.Errorf("tool %q was allowed", tool)
		}
	}
	if reached {
		t.Fatal("a denied call reached the upstream server")
	}

	if _, err := p.Call(context.Background(), "jira", "get_issue", nil); err != nil {
		t.Fatalf("an allowed tool was refused: %v", err)
	}
}

func TestBearerAuth(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{Name: "x", URL: srv.URL, Auth: AuthBearer, Secret: "sekrit"}})
	if _, err := p.Call(context.Background(), "x", "t", nil); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer sekrit" {
		t.Fatalf("authorization = %q", got)
	}
}

// Streamable-HTTP endpoints answer with SSE framing even for a unary call.
func TestSSEResponseHandled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"value\":42}}\n\n"))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{Name: "x", URL: srv.URL}})
	out, err := p.Call(context.Background(), "x", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "42") {
		t.Fatalf("result = %s", out)
	}
}

func TestRPCErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"no such issue"}}`))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{Name: "x", URL: srv.URL}})
	if _, err := p.Call(context.Background(), "x", "t", nil); err == nil ||
		!strings.Contains(err.Error(), "no such issue") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownServerRefused(t *testing.T) {
	p := New()
	if _, err := p.Call(context.Background(), "ghost", "t", nil); err == nil {
		t.Fatal("an unconfigured server was called")
	}
	if p.Has("ghost") {
		t.Error("Has reported an unconfigured server")
	}
}

func TestHTTPErrorIncludesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	}))
	defer srv.Close()

	p := New()
	p.Configure([]Server{{Name: "x", URL: srv.URL}})
	_, err := p.Call(context.Background(), "x", "t", nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the status included", err)
	}
}
