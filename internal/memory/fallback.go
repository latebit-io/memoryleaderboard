package memory

import (
	"context"
	"log/slog"
)

// FallbackStore searches with primary and retries with secondary when
// primary fails or finds nothing. Degradation is a service-availability
// decision, so it lives in composition rather than inside a strategy.
type FallbackStore struct {
	Store // writes go to primary
	primary,
	secondary Store
}

// WithFallback composes two stores into one whose Search degrades from
// primary to secondary. Writes go to primary.
func WithFallback(primary, secondary Store) *FallbackStore {
	return &FallbackStore{Store: primary, primary: primary, secondary: secondary}
}

// Search tries primary, then secondary. An empty primary result is worth
// retrying: an agentic strategy that gives up finds nothing in exactly
// the cases a plain catalog lookup still answers.
func (f *FallbackStore) Search(ctx context.Context, userID, query string, topK int) ([]Record, error) {
	records, err := f.primary.Search(ctx, userID, query, topK)
	if err == nil && len(records) > 0 {
		return records, nil
	}
	if err != nil {
		// Backend errors can contain the user's scoped document path.
		slog.WarnContext(ctx, "primary search fell back")
	}
	return f.secondary.Search(ctx, userID, query, topK)
}
