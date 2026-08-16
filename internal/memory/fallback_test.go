package memory

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

type searchStore struct {
	records []Record
	err     error
}

func (s searchStore) Add(context.Context, string, string, string, []Message) error { return nil }

func (s searchStore) Search(context.Context, string, string, int) ([]Record, error) {
	return s.records, s.err
}

func TestFallbackLogDoesNotExposeUserPath(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	primary := searchStore{err: fmt.Errorf("fetch /u/private-user/doc.md: status error")}
	secondary := searchStore{records: []Record{{ID: "fallback"}}}
	if _, err := WithFallback(primary, secondary).Search(context.Background(), "private-user", "q", 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "private-user") || strings.Contains(logs.String(), "/u/") {
		t.Fatalf("fallback log exposed user path: %s", logs.String())
	}
}
