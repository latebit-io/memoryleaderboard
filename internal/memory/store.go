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
	ID        string  `json:"id"`
	Content   string  `json:"content"`
	Score     float64 `json:"score,omitempty"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// Store persists conversations and returns ranked evidence.
// user_id is the isolation boundary on both paths.
type Store interface {
	Add(ctx context.Context, userID, sessionID, requestID string, msgs []Message) error
	Search(ctx context.Context, userID, query string, topK int) ([]Record, error)
}
