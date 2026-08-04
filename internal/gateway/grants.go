package gateway

import (
	"sync"
	"time"
)

// Session grants are the middle layer between prompting for everything and
// running unattended: a human approves a tool once and is not asked again for
// the rest of that thread's session.
//
// They live in memory only. A gateway restart drops them, which is the correct
// direction to fail — a grant nobody remembers issuing should not survive.

type sessionGrant struct {
	Tool    string
	By      string
	Granted time.Time
}

type grantStore struct {
	mu sync.RWMutex
	// thread id -> tool name -> grant
	byThread map[string]map[string]sessionGrant
}

func newGrantStore() *grantStore {
	return &grantStore{byThread: map[string]map[string]sessionGrant{}}
}

// Grant records that a tool may proceed unprompted for this thread.
//
// Scoped to the tool name rather than the exact arguments: "allowed for this
// session" is what the button says, and a grant narrow enough to match only the
// one command would never fire again, making the button useless. Deny rules are
// still evaluated first, so a grant can never unlock something denied.
func (g *grantStore) Grant(threadID, tool, by string) {
	if threadID == "" || tool == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byThread[threadID] == nil {
		g.byThread[threadID] = map[string]sessionGrant{}
	}
	g.byThread[threadID][tool] = sessionGrant{Tool: tool, By: by, Granted: time.Now()}
}

// Held reports an existing grant for a tool in a thread.
func (g *grantStore) Held(threadID, tool string) (sessionGrant, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	byTool, ok := g.byThread[threadID]
	if !ok {
		return sessionGrant{}, false
	}
	gr, ok := byTool[tool]
	return gr, ok
}

// Clear drops every grant for a thread. Called when the session it was scoped to
// ends — `!new`, `!rebind`, or a bundle change that invalidates the session.
func (g *grantStore) Clear(threadID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.byThread[threadID])
	delete(g.byThread, threadID)
	return n
}

// Tools lists the tools granted in a thread, for status output.
func (g *grantStore) Tools(threadID string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.byThread[threadID]))
	for tool := range g.byThread[threadID] {
		out = append(out, tool)
	}
	return out
}
