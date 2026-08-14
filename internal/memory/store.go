// Package memory defines the storage contract the adapter exposes to the
// leaderboard harness, independent of the demarkus backend.
package memory

import "context"

// Message is one conversation turn from an Add request.
type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Record is one ranked memory evidence item in a Search response.
type Record struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	// Score is a rank hint only: strictly descending across a result set,
	// with no meaning across implementations or requests. The benchmark
	// ranks by array order, so nothing downstream compares two searches'
	// scores.
	Score     float64 `json:"score,omitempty"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// Store persists conversations and returns ranked evidence.
// user_id is the isolation boundary on both paths.
type Store interface {
	Add(ctx context.Context, userID, sessionID, requestID string, msgs []Message) error
	Search(ctx context.Context, userID, query string, topK int) ([]Record, error)
}

// Navigator is the read surface a search strategy drives: the document
// operations of a backend, scoped by the caller to one user's subtree.
// Kept separate from Store so a strategy depends on document access
// rather than on a concrete backend.
type Navigator interface {
	// Scope returns the root path of a user's documents.
	Scope(userID string) string
	// Lookup matches a subject against the catalog under scope and
	// returns the backend's ranked result table.
	Lookup(ctx context.Context, scope, query string, limit int) (string, error)
	// List returns a directory listing.
	List(ctx context.Context, path string) (string, error)
	// FetchRecord reads one document as an evidence record.
	FetchRecord(ctx context.Context, path string) (Record, error)
}
