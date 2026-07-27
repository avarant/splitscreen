// Package surface abstracts the chat platforms a gateway talks to.
//
// This is one of the two plug points in the system, symmetric with harness
// adapters on the other side. The gateway holds every surface credential;
// runners never call a chat API.
package surface

import (
	"context"
	"io"

	"github.com/avarant/splitscreen/protocol"
)

// User is a person on a surface.
type User struct {
	ID      string
	Display string
}

// Ref identifies a posted message so it can be edited in place. Streaming
// output is an edit loop, not a series of posts.
type Ref struct {
	Channel string
	Thread  string
	ID      string
}

// Inbound is a normalized user message.
type Inbound struct {
	Surface string
	Channel string
	// Thread is the conversation key. Surfaces without native threads use the
	// channel id, which makes the whole channel one conversation.
	Thread string
	User   User
	Text   string
	IsDM   bool
	Files  []File
}

// File is an attachment on an inbound message. Open is called at most once and
// the gateway is responsible for closing the reader.
type File struct {
	Name string
	Mime string
	Size int64
	Open func(context.Context) (io.ReadCloser, error)
}

// Decision is a permission prompt resolved by a human.
type Decision struct {
	RequestID string
	Decision  protocol.Decision
	User      User
	Ref       Ref
}

// Handler receives everything a surface produces. Implemented by the gateway.
type Handler interface {
	OnMessage(context.Context, Inbound)
	OnDecision(context.Context, Decision)
}

// Persona is the display identity a runner posts under. One bot user can wear
// many personas; the tradeoff is that a persona cannot be @-mentioned
// individually, which is why routing is channel-based.
type Persona struct {
	Name string
	Icon string
}

// Post is an outbound message.
type Post struct {
	Channel string
	Thread  string
	Text    string
	Persona Persona
	// Ephemeral messages are visible only to User, used for notices that would
	// be noise in a shared thread.
	Ephemeral bool
	User      string
}

// Prompt is a permission request rendered as interactive controls.
type Prompt struct {
	Channel   string
	Thread    string
	Persona   Persona
	RequestID string
	Tool      string
	Summary   string
	Detail    string
}

// Upload is a file sent from a runner to the surface.
type Upload struct {
	Channel string
	Thread  string
	Name    string
	Mime    string
	Comment string
	Content io.Reader
	Size    int64
}

// Surface is a chat platform adapter.
type Surface interface {
	// Name identifies the adapter ("slack"). Used in thread keys and audit rows.
	Name() string
	// Start connects and blocks until ctx is cancelled. Inbound traffic is
	// delivered to h.
	Start(ctx context.Context, h Handler) error
	// Post sends a new message and returns a ref for later edits.
	Post(ctx context.Context, p Post) (Ref, error)
	// Update edits a previously posted message in place.
	Update(ctx context.Context, ref Ref, p Post) error
	// Prompt posts an interactive permission request.
	Prompt(ctx context.Context, p Prompt) (Ref, error)
	// Resolve replaces a prompt with its outcome, so a thread does not keep
	// live buttons for an already-decided request.
	Resolve(ctx context.Context, ref Ref, text string) error
	// Upload sends a file.
	Upload(ctx context.Context, u Upload) error
	// Close releases the connection.
	Close() error
}
