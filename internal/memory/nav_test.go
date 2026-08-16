package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/latebit-io/nib/ai/llm"
)

// scriptedProvider replays a fixed sequence of tool-call turns, then a
// plain text turn, so the nav loop runs without an LLM.
type scriptedProvider struct {
	turns     [][]llm.ToolCall
	turn      int
	failAfter int
}

func (p *scriptedProvider) Stream(_ context.Context, _ []llm.Message, _ []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	if p.failAfter > 0 && p.turn >= p.failAfter {
		return nil, fmt.Errorf("scripted provider failure")
	}
	ch := make(chan llm.StreamEvent, 1)
	if p.turn < len(p.turns) {
		ch <- llm.StreamEvent{Done: true, ToolCalls: p.turns[p.turn]}
		p.turn++
	} else {
		ch <- llm.StreamEvent{Done: true, Token: "done"}
	}
	close(ch)
	return ch, nil
}

func call(id, name string, args any) llm.ToolCall {
	raw, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: string(raw)}}
}

// fakeNav is an in-memory Navigator: path -> body.
type fakeNav struct {
	docs        map[string]string
	fetches     int
	lookupLimit int
}

func (f *fakeNav) Scope(userID string) string { return "/u/" + userID }

func (f *fakeNav) Lookup(_ context.Context, scope, query string, limit int) (string, error) {
	f.lookupLimit = limit
	var b strings.Builder
	b.WriteString("| Path | Importance |\n")
	for path, body := range f.docs {
		if strings.HasPrefix(path, scope) && strings.Contains(body, query) {
			fmt.Fprintf(&b, "| %s | 0.5 |\n", path)
		}
	}
	return b.String(), nil
}

func (f *fakeNav) List(_ context.Context, path string) (string, error) {
	var b strings.Builder
	for p := range f.docs {
		if strings.HasPrefix(p, path) {
			b.WriteString(p + "\n")
		}
	}
	return b.String(), nil
}

func (f *fakeNav) FetchRecord(_ context.Context, path string) (Record, error) {
	body, ok := f.docs[path]
	if !ok {
		return Record{}, fmt.Errorf("fetch %s: status not-found", path)
	}
	f.fetches++
	return Record{ID: path, Content: body, CreatedAt: "2026-08-14T00:00:00Z"}, nil
}

// errStore fails every call; the nav path must not touch it.
type errStore struct{}

func (errStore) Add(context.Context, string, string, string, []Message) error { return errUnused }
func (errStore) Search(context.Context, string, string, int) ([]Record, error) {
	return nil, errUnused
}

var errUnused = fmt.Errorf("store should not be called")

func newFakeNavStore(t *testing.T, docs map[string]string, turns [][]llm.ToolCall) (*NavStore, *fakeNav) {
	t.Helper()
	nav := &fakeNav{docs: docs}
	return NewNavStore(errStore{}, nav, &scriptedProvider{turns: turns}, NavOptions{}), nav
}

func TestSearchReturnsSubmittedOrder(t *testing.T) {
	docs := map[string]string{
		"/u/u1/a.md": "alpha evidence",
		"/u/u1/b.md": "beta evidence",
	}
	store, nav := newFakeNavStore(t, docs, [][]llm.ToolCall{
		{call("1", "memory_lookup", map[string]any{"query": "evidence"})},
		{call("2", "memory_fetch", map[string]any{"path": "/u/u1/a.md"}),
			call("3", "memory_fetch", map[string]any{"path": "/u/u1/b.md"})},
		// Submitted order is the reverse of fetch order.
		{call("4", submitToolName, map[string]any{"paths": []string{"/u/u1/b.md", "/u/u1/a.md"}})},
	})

	records, err := store.Search(context.Background(), "u1", "evidence", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "/u/u1/b.md" || records[1].ID != "/u/u1/a.md" {
		t.Fatalf("records = %+v, want submitted order", records)
	}
	if records[0].Score <= records[1].Score {
		t.Errorf("scores not descending: %v", records)
	}
	// Evidence carries the full body, not the agent's snippet.
	if records[0].Content != "beta evidence" {
		t.Errorf("content = %q, want full body", records[0].Content)
	}
	if nav.fetches != 2 {
		t.Errorf("fetches = %d, want 2", nav.fetches)
	}
}

func TestSearchFallsBackToFetchOrderWithoutSubmit(t *testing.T) {
	docs := map[string]string{"/u/u1/a.md": "alpha", "/u/u1/b.md": "beta"}
	store, _ := newFakeNavStore(t, docs, [][]llm.ToolCall{
		{call("1", "memory_fetch", map[string]any{"path": "/u/u1/a.md"})},
		// No submit: the model just stops, which parks the loop.
	})

	records, err := store.Search(context.Background(), "u1", "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "/u/u1/a.md" {
		t.Fatalf("records = %+v, want the fetched document", records)
	}
}

func TestSearchReturnsFetchedEvidenceAfterProviderFailure(t *testing.T) {
	nav := &fakeNav{docs: map[string]string{"/u/u1/a.md": "alpha"}}
	provider := &scriptedProvider{
		turns:     [][]llm.ToolCall{{call("1", "memory_fetch", map[string]any{"path": "/u/u1/a.md"})}},
		failAfter: 1,
	}
	store := NewNavStore(errStore{}, nav, provider, NavOptions{})

	records, err := store.Search(context.Background(), "u1", "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "/u/u1/a.md" {
		t.Fatalf("records = %+v, want fetched evidence", records)
	}
}

func TestSearchErrorsWhenNothingRead(t *testing.T) {
	store, _ := newFakeNavStore(t, map[string]string{"/u/u1/a.md": "alpha"}, nil)
	if _, err := store.Search(context.Background(), "u1", "alpha", 10); err == nil {
		t.Fatal("want an error when the agent reads nothing")
	}
}

func TestSearchTopKTruncates(t *testing.T) {
	docs := map[string]string{"/u/u1/a.md": "a", "/u/u1/b.md": "b", "/u/u1/c.md": "c"}
	store, _ := newFakeNavStore(t, docs, [][]llm.ToolCall{
		{call("1", "memory_fetch", map[string]any{"path": "/u/u1/a.md"}),
			call("2", "memory_fetch", map[string]any{"path": "/u/u1/b.md"}),
			call("3", "memory_fetch", map[string]any{"path": "/u/u1/c.md"})},
		{call("4", submitToolName, map[string]any{"paths": []string{"/u/u1/c.md", "/u/u1/b.md", "/u/u1/a.md"}})},
	})

	records, err := store.Search(context.Background(), "u1", "x", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
}

func TestEvidenceRejectsNonPositiveTopK(t *testing.T) {
	rec := Record{ID: "/u/u1/a.md"}
	for name, run := range map[string]*navRun{
		"fetch order":     {fetched: []Record{rec}},
		"submitted order": {fetched: []Record{rec}, ranked: []string{rec.ID}},
	} {
		for _, topK := range []int{0, -1} {
			if got := run.evidence(topK); len(got) != 0 {
				t.Errorf("%s evidence(%d) = %+v, want none", name, topK, got)
			}
		}
	}
}

func TestNavOptionsDefaultNonPositiveValues(t *testing.T) {
	opts := (NavOptions{Budget: -1, MaxTurns: -1, SnippetBytes: -1}).withDefaults()
	if opts.Budget <= 0 || opts.MaxTurns <= 0 || opts.SnippetBytes <= 0 {
		t.Fatalf("defaults left non-positive values: %+v", opts)
	}
}

func TestLookupClampsModelLimit(t *testing.T) {
	nav := &fakeNav{docs: map[string]string{}}
	run := &navRun{nav: nav, scope: "/u/u1"}
	if _, err := run.lookup(context.Background(), map[string]any{"query": "alpha", "limit": float64(1000)}); err != nil {
		t.Fatal(err)
	}
	if nav.lookupLimit != maxLookupLimit {
		t.Errorf("lookup limit = %d, want %d", nav.lookupLimit, maxLookupLimit)
	}
}

func TestUnfetchedSubmissionsDropped(t *testing.T) {
	docs := map[string]string{"/u/u1/a.md": "alpha"}
	store, _ := newFakeNavStore(t, docs, [][]llm.ToolCall{
		{call("1", "memory_fetch", map[string]any{"path": "/u/u1/a.md"})},
		{call("2", submitToolName, map[string]any{"paths": []string{"/u/u1/never-read.md", "/u/u1/a.md"}})},
	})

	records, err := store.Search(context.Background(), "u1", "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "/u/u1/a.md" {
		t.Fatalf("records = %+v, want only the fetched document", records)
	}
}

func TestToolsCannotEscapeUserScope(t *testing.T) {
	r := &navRun{scope: "/u/abc", nav: &fakeNav{docs: map[string]string{}}}
	for _, ok := range []string{"/u/abc/sessions/x.md", "sessions/x.md", "/u/abc"} {
		if _, err := r.scoped(ok); err != nil {
			t.Errorf("scoped(%q) failed: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"/u/other/x.md",
		"/u/abcd/x.md",
		"/u/abc/../other/x.md",
		"../other/x.md",
		"/u/abc/../../etc/passwd",
	} {
		if p, err := r.scoped(bad); err == nil {
			t.Errorf("scoped(%q) = %q, want error", bad, p)
		}
	}
}

func TestFetchShowsSnippetButKeepsFullBody(t *testing.T) {
	body := strings.Repeat("x", 500) + "TAIL"
	nav := &fakeNav{docs: map[string]string{"/u/u1/big.md": body}}
	r := &navRun{nav: nav, scope: "/u/u1", snippet: 100}

	out, err := r.fetch(context.Background(), map[string]any{"path": "/u/u1/big.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 200 || strings.Contains(out, "TAIL") {
		t.Errorf("agent saw %d bytes, want a snippet", len(out))
	}
	if got := r.fetched[0].Content; got != body {
		t.Errorf("stored body truncated: %d bytes", len(got))
	}

	// A repeat fetch is served from what was already read.
	if _, err := r.fetch(context.Background(), map[string]any{"path": "/u/u1/big.md"}); err != nil {
		t.Fatal(err)
	}
	if nav.fetches != 1 {
		t.Errorf("fetches = %d, want the second to be served locally", nav.fetches)
	}
}

func TestSnippetCutsOnRuneBoundary(t *testing.T) {
	// Every rune is 3 bytes, so a 10-byte limit lands mid-rune.
	out := snippet(strings.Repeat("日", 10), 10)
	if !utf8.ValidString(out) {
		t.Fatalf("snippet produced invalid UTF-8: %q", out)
	}
	if !strings.HasSuffix(out, "\n[truncated]") {
		t.Fatalf("snippet = %q, want truncation marker", out)
	}
	if got := strings.TrimSuffix(out, "\n[truncated]"); got != strings.Repeat("日", 3) {
		t.Errorf("kept %q, want three whole runes", got)
	}
}
