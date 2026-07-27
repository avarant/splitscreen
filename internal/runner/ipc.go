package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The runner exposes a unix socket, not a TCP port. Two runners on one box
// therefore cannot collide, and the port-in-use failure mode that plagued the
// single-process bridge cannot recur.
//
// Local clients are: the git credential helper, the permission-prompt MCP shim,
// the proxied-MCP shim, and the file sender. All of them are short-lived
// subprocesses that speak newline-delimited JSON.

// IPCRequest is one local call.
type IPCRequest struct {
	Op string `json:"op"`

	// Thread scopes a request to a conversation. Shims are spawned per session
	// with their thread baked into argv, so a local helper never has to guess.
	Thread string `json:"thread,omitempty"`

	// credential
	Resource string `json:"resource,omitempty"`

	// permission
	Tool      string          `json:"tool,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`

	// mcp
	Server string          `json:"server,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`

	// send-file
	Path    string `json:"path,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// IPCResponse is the reply.
type IPCResponse struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

const (
	OpCredential = "credential"
	OpPermission = "permission"
	OpMCPCall    = "mcp.call"
	OpMCPList    = "mcp.list"
	OpSendFile   = "send-file"
	OpPing       = "ping"
)

// SocketPath returns the runtime socket for a runner.
func SocketPath(root, name string) string {
	return filepath.Join(root, name, "run.sock")
}

type ipcServer struct {
	r  *Runner
	ln net.Listener

	mu      sync.Mutex
	stopped bool
}

func (r *Runner) startIPC() error {
	path := SocketPath(r.opts.RuntimeRoot, r.opts.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("runner: create runtime dir: %w", err)
	}
	// A stale socket from an ungraceful exit would otherwise make bind fail
	// forever.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runner: clear stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("runner: listen on %s: %w", path, err)
	}
	// Only the runner's own user may speak to it. Local helpers run as that
	// user; nothing else has business here.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("runner: secure socket: %w", err)
	}
	r.ipc = &ipcServer{r: r, ln: ln}
	go r.ipc.serve()
	return nil
}

func (s *ipcServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return
			}
			s.r.log.Warn("ipc accept failed", "err", err)
			return
		}
		go s.handle(conn)
	}
}

func (s *ipcServer) close() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	_ = s.ln.Close()
}

func (s *ipcServer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Minute)) // permission waits can be long

	dec := json.NewDecoder(bufio.NewReader(c))
	var req IPCRequest
	if err := dec.Decode(&req); err != nil {
		writeIPC(c, IPCResponse{Error: "malformed request: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 19*time.Minute)
	defer cancel()

	resp := s.r.handleIPC(ctx, req)
	writeIPC(c, resp)
}

func writeIPC(c net.Conn, resp IPCResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(IPCResponse{Error: "could not encode response"})
	}
	_, _ = c.Write(append(b, '\n'))
}

// handleIPC dispatches a local helper's request. Every path here ends up asking
// the gateway: the runner itself holds no credentials and makes no policy
// decisions.
func (r *Runner) handleIPC(ctx context.Context, req IPCRequest) IPCResponse {
	switch req.Op {
	case OpPing:
		return IPCResponse{OK: true}

	case OpCredential:
		cred, err := r.requestCredential(ctx, req.Resource)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		data, _ := json.Marshal(cred)
		return IPCResponse{OK: true, Data: data}

	case OpPermission:
		decision, err := r.requestPermission(ctx, req.Thread, req.Tool, req.Input)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		data, _ := json.Marshal(decision)
		return IPCResponse{OK: true, Data: data}

	case OpMCPCall:
		result, err := r.callProxiedMCP(ctx, req.Thread, req.Server, req.Tool, req.Args)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Data: result}

	case OpMCPList:
		result, err := r.listProxiedMCP(ctx, req.Thread, req.Server)
		if err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true, Data: result}

	case OpSendFile:
		if err := r.sendFile(ctx, req.Thread, req.Path, req.Comment); err != nil {
			return IPCResponse{Error: err.Error()}
		}
		return IPCResponse{OK: true}

	default:
		return IPCResponse{Error: "unknown op " + req.Op}
	}
}

// CallIPC is the client side, used by the helper subcommands.
func CallIPC(socket string, req IPCRequest, timeout time.Duration) (IPCResponse, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return IPCResponse{}, fmt.Errorf("splitscreen: cannot reach the runner at %s: %w", socket, err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	body, err := json.Marshal(req)
	if err != nil {
		return IPCResponse{}, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return IPCResponse{}, err
	}

	var resp IPCResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return IPCResponse{}, fmt.Errorf("splitscreen: no reply from the runner: %w", err)
	}
	if !resp.OK && resp.Error == "" {
		return resp, errors.New("splitscreen: request failed without a reason")
	}
	return resp, nil
}
