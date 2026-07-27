// Package mcpstdio implements the server side of MCP over stdio.
//
// Two shims use it: the permission-prompt server every session runs, and the
// proxy shim that fronts a credential-bearing server living on the gateway.
// Both are thin — they translate stdio JSON-RPC into a unix-socket request to
// the local runner, which forwards to the gateway.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// ProtocolVersion is the MCP revision these shims advertise.
const ProtocolVersion = "2024-11-05"

// Tool is a tool the shim exposes.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Handler serves tool listing and invocation.
type Handler interface {
	Tools(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeStdio runs the loop over the process's standard streams.
func ServeStdio(ctx context.Context, name string, h Handler) error {
	return Serve(ctx, name, h, os.Stdin, os.Stdout)
}

// Serve runs the loop until in reaches EOF.
//
// Streams are parameters rather than globals so the loop is testable without
// swapping os.Stdout out from under the process.
func Serve(ctx context.Context, name string, h Handler, in io.Reader, out io.Writer) error {
	// Line-delimited, not a streaming json.Decoder. A Decoder cannot resync
	// after malformed input: it returns the same syntax error forever, so
	// skipping a bad message would spin instead of recovering.
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)

	var writeMu sync.Mutex
	write := func(resp response) {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(resp)
		if err != nil {
			return
		}
		_, _ = out.Write(append(b, '\n'))
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// A malformed line is not fatal: the next one may be fine.
			continue
		}

		// Notifications carry no id and expect no reply.
		isNotification := len(req.ID) == 0

		switch req.Method {
		case "initialize":
			if isNotification {
				continue
			}
			write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": name, "version": "1.0"},
			}})

		case "notifications/initialized", "initialized":
			// nothing to do

		case "ping":
			if !isNotification {
				write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
			}

		case "tools/list":
			if isNotification {
				continue
			}
			tools, err := h.Tools(ctx)
			if err != nil {
				write(response{JSONRPC: "2.0", ID: req.ID,
					Error: &rpcError{Code: -32603, Message: err.Error()}})
				continue
			}
			if tools == nil {
				tools = []Tool{}
			}
			write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})

		case "tools/call":
			if isNotification {
				continue
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				write(response{JSONRPC: "2.0", ID: req.ID,
					Error: &rpcError{Code: -32602, Message: "bad params: " + err.Error()}})
				continue
			}
			result, err := h.Call(ctx, params.Name, params.Arguments)
			if err != nil {
				// Tool errors are returned as results with isError, not as
				// transport errors: the model should see and react to them.
				write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
					"content": []any{map[string]any{"type": "text", "text": err.Error()}},
					"isError": true,
				}})
				continue
			}
			write(response{JSONRPC: "2.0", ID: req.ID, Result: wrapContent(result)})

		default:
			if !isNotification {
				write(response{JSONRPC: "2.0", ID: req.ID,
					Error: &rpcError{Code: -32601, Message: "unknown method " + req.Method}})
			}
		}
	}
	return sc.Err()
}

// maxLine bounds one JSON-RPC message. Tool arguments can be large; a scanner
// default of 64KiB would truncate them into unexplained parse failures.
const maxLine = 16 << 20

// wrapContent passes an upstream MCP result through untouched, and wraps a bare
// payload in the content envelope the protocol expects.
func wrapContent(result json.RawMessage) any {
	if len(result) == 0 {
		return map[string]any{"content": []any{}}
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(result, &probe); err == nil {
		if _, ok := probe["content"]; ok {
			return json.RawMessage(result)
		}
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(result)}},
	}
}

// TextResult builds a single-text-block result.
func TextResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
	return b
}

// JSONResult marshals v into a single text block, which is how MCP conveys
// structured payloads.
func JSONResult(v any) (json.RawMessage, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("mcpstdio: marshal result: %w", err)
	}
	return TextResult(string(body)), nil
}
