package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/agent/event"
	"github.com/latebit-io/nib/ai/llm"
)

// DefaultNavPrompt instructs the navigation agent. Exported so an eval
// run can vary the prompt without editing this package.
const DefaultNavPrompt = `You are a memory-retrieval navigator over a document store.
A user's memories live under one directory subtree as markdown documents.
Given a query, find the documents that are the best EVIDENCE for answering
it. You do not answer the query yourself.

Method:
1. memory_lookup matches the query's subjects against document tags and titles. Try the query terms, then synonyms or related subjects if results are thin.
2. memory_list explores directories when lookup misses (lookup only finds tagged/titled subjects).
3. memory_fetch reads a document. Only fetched documents can be submitted.
4. When done, call submit_evidence ONCE with document paths ordered most-relevant first. Submit every document that plausibly bears on the query, best first — recall beats precision at the tail.

Budget your calls; a handful of lookups and the fetches that matter.`

// NavOptions tunes one navigation strategy. The zero value is usable;
// each field falls back to its default.
type NavOptions struct {
	// SystemPrompt steers the agent. Defaults to [DefaultNavPrompt].
	SystemPrompt string
	// Budget bounds one search. Defaults to 90s. The request context's
	// own deadline still applies and wins when it is shorter.
	Budget time.Duration
	// MaxTurns caps the agent loop. Defaults to 16: enough for several
	// lookups, a dozen fetches, and the submit.
	MaxTurns int
	// SnippetBytes is how much of a document the agent sees per fetch.
	// The full body still reaches the caller as evidence; the agent only
	// needs enough to judge relevance, and every byte it reads is
	// re-sent on each subsequent turn. Defaults to 2048.
	SnippetBytes int
}

func (o NavOptions) withDefaults() NavOptions {
	if o.SystemPrompt == "" {
		o.SystemPrompt = DefaultNavPrompt
	}
	if o.Budget <= 0 {
		o.Budget = 90 * time.Second
	}
	if o.MaxTurns <= 0 {
		o.MaxTurns = 16
	}
	if o.SnippetBytes <= 0 {
		o.SnippetBytes = 2048
	}
	return o
}

// NavStore answers Search with a navigation agent that walks the user's
// subtree via lookup/list/fetch tools and submits ranked evidence. Add
// passes through to the embedded Store; wrap with [WithFallback] to
// degrade to that store's own Search when navigation comes up empty.
type NavStore struct {
	Store
	nav      Navigator
	provider llm.Provider
	opts     NavOptions
}

// NewNavStore builds a navigation strategy over nav, passing writes
// through to store.
func NewNavStore(store Store, nav Navigator, provider llm.Provider, opts NavOptions) *NavStore {
	return &NavStore{Store: store, nav: nav, provider: provider, opts: opts.withDefaults()}
}

// errNoEvidence reports a run that finished without submitting anything.
var errNoEvidence = errors.New("nav agent submitted no evidence")

// Search runs the navigation agent. A run that ends early still returns
// whatever it read, since partial evidence still scores.
func (n *NavStore) Search(ctx context.Context, userID, query string, topK int) ([]Record, error) {
	ctx, cancel := context.WithTimeout(ctx, n.opts.Budget)
	defer cancel()

	run := &navRun{nav: n.nav, scope: n.nav.Scope(userID), snippet: n.opts.SnippetBytes}

	// The drain must outlive the loop: the agent blocks briefly on
	// control events, and a full channel would stall the run.
	events := make(chan event.Event, 8)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()

	a, err := agent.New(agent.Options{
		Provider:     n.provider,
		Events:       events,
		SystemPrompt: n.opts.SystemPrompt,
		Tools:        run.tools(),
		MaxTurns:     n.opts.MaxTurns,
		Hooks: agent.Hooks{
			// submit_evidence is the intended exit: end the run on it
			// rather than letting the loop take another turn.
			AfterToolCall: func(_ context.Context, c agent.AfterToolCallInput) (agent.AfterToolCallResult, error) {
				return agent.AfterToolCallResult{Terminate: c.Name == submitToolName}, nil
			},
			// A model that stops without submitting would otherwise park
			// waiting for input it will never get; an error ends the run.
			BeforePark: func(context.Context) (event.AgentParked, error) {
				return event.AgentParked{}, errNoEvidence
			},
		},
	})
	if err == nil {
		err = a.Prompt(ctx, fmt.Sprintf("Query: %s\n\nFind and submit up to %d evidence documents.", query, topK))
	}
	if err == nil {
		a.WaitForIdle()
	}
	close(events)
	<-drained

	records := run.evidence(topK)
	if len(records) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.Join(errNoEvidence, ctx.Err())
	}
	return records, nil
}

// navRun is one search's tool state: the user's scope and what was read.
// Not synchronized: the agent loop dispatches tool calls serially.
type navRun struct {
	nav     Navigator
	scope   string
	snippet int
	fetched []Record // fetch order, the ranking when no submit arrives
	ranked  []string // submit_evidence order
}

// scoped validates an agent-supplied path against the user's subtree.
// The model never sees another user's prefix, but path discipline is the
// isolation boundary, so enforce it here too.
func (r *navRun) scoped(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		p = r.scope + "/" + p
	}
	// Clean resolves any traversal before the prefix test, so ".." cannot
	// climb out of the scope by spelling.
	trailing := strings.HasSuffix(p, "/")
	p = path.Clean(p)
	if trailing {
		p += "/"
	}
	if p != r.scope && !strings.HasPrefix(p, r.scope+"/") {
		return "", fmt.Errorf("path %s is outside your memory scope", p)
	}
	return p, nil
}

func (r *navRun) record(docPath string) (Record, bool) {
	for _, rec := range r.fetched {
		if rec.ID == docPath {
			return rec, true
		}
	}
	return Record{}, false
}

// evidence assembles the ranked result: submitted order when the agent
// submitted, fetch order otherwise.
func (r *navRun) evidence(topK int) []Record {
	if topK <= 0 {
		return nil
	}
	if len(r.ranked) == 0 {
		out := r.fetched
		if len(out) > topK {
			out = out[:topK]
		}
		return withRankScores(out)
	}
	out := make([]Record, 0, min(len(r.ranked), topK))
	for _, p := range r.ranked {
		rec, ok := r.record(p)
		if !ok {
			continue // never fetched; the agent cannot vouch for it
		}
		out = append(out, rec)
		if len(out) == topK {
			break
		}
	}
	return withRankScores(out)
}

// withRankScores stamps a descending rank hint; the benchmark ranks by
// array order, so only the ordering carries meaning.
func withRankScores(recs []Record) []Record {
	for i := range recs {
		recs[i].Score = 1.0 / float64(i+1)
	}
	return recs
}

const submitToolName = "submit_evidence"

// maxLookupLimit bounds model-controlled transcript growth.
const maxLookupLimit = 50

// Tool definitions are constant; only the bound run varies per search.
var navToolDefs = []llm.ToolDef{
	toolDef("memory_lookup", "Look up documents by subject against the catalog. Matches tags and titles, returns a ranked table of (path, importance, title, tags). Not full-text search.",
		map[string]llm.FunctionParam{
			"query": {Type: "string", Description: "subject to look up"},
			"limit": {Type: "integer", Description: "max results (default 20)"},
		}, "query"),
	toolDef("memory_list", "List documents and subdirectories at a path in your memory subtree.",
		map[string]llm.FunctionParam{
			"path": {Type: "string", Description: "directory path, e.g. /sessions/ (relative to your scope)"},
		}, "path"),
	toolDef("memory_fetch", "Fetch a document by path. Returns the opening of the document, enough to judge relevance. Only fetched documents can be submitted.",
		map[string]llm.FunctionParam{
			"path": {Type: "string", Description: "document path from a lookup or list result"},
		}, "path"),
	toolDef(submitToolName, "Submit the final evidence: fetched document paths ordered most-relevant first. Call exactly once, then stop.",
		map[string]llm.FunctionParam{
			"paths": {Type: "array", Description: "document paths, best first", Items: &llm.FunctionParam{Type: "string"}},
		}, "paths"),
}

func toolDef(name, description string, params map[string]llm.FunctionParam, required ...string) llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        name,
			Description: description,
			Parameters:  llm.FunctionParams{Type: "object", Properties: params, Required: required},
		},
	}
}

func (r *navRun) tools() []agent.Tool {
	runners := map[string]func(context.Context, map[string]any) (string, error){
		"memory_lookup": r.lookup,
		"memory_list":   r.list,
		"memory_fetch":  r.fetch,
		submitToolName:  r.submit,
	}
	tools := make([]agent.Tool, 0, len(navToolDefs))
	for _, def := range navToolDefs {
		run, ok := runners[def.Function.Name]
		if !ok {
			panic("nav tool without a runner: " + def.Function.Name)
		}
		tools = append(tools, &navTool{def: def, run: run})
	}
	return tools
}

func (r *navRun) lookup(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	limit = min(limit, maxLookupLimit)
	table, err := r.nav.Lookup(ctx, r.scope+"/", query, limit)
	if err != nil {
		return "", err
	}
	if table == "" {
		return "no documents in scope yet", nil
	}
	return table, nil
}

func (r *navRun) list(ctx context.Context, args map[string]any) (string, error) {
	p, _ := args["path"].(string)
	docPath, err := r.scoped(p)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(docPath, "/") {
		docPath += "/"
	}
	listing, err := r.nav.List(ctx, docPath)
	if err != nil {
		return "", err
	}
	return snippet(listing, min(r.snippet, maxListBytes)), nil
}

// fetch reads a document, keeps the full body for the caller, and shows
// the agent only its opening.
func (r *navRun) fetch(ctx context.Context, args map[string]any) (string, error) {
	p, _ := args["path"].(string)
	docPath, err := r.scoped(p)
	if err != nil {
		return "", err
	}
	if rec, ok := r.record(docPath); ok {
		return snippet(rec.Content, r.snippet), nil
	}
	rec, err := r.nav.FetchRecord(ctx, docPath)
	if err != nil {
		return "", err
	}
	r.fetched = append(r.fetched, rec)
	return snippet(rec.Content, r.snippet), nil
}

func (r *navRun) submit(_ context.Context, args map[string]any) (string, error) {
	raw, ok := args["paths"].([]any)
	if !ok {
		return "", fmt.Errorf("paths must be an array of strings")
	}
	r.ranked = nil
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		docPath, err := r.scoped(s)
		if err != nil || seen[docPath] {
			continue
		}
		seen[docPath] = true
		r.ranked = append(r.ranked, docPath)
	}
	return fmt.Sprintf("recorded %d evidence documents; you are done", len(r.ranked)), nil
}

func snippet(body string, limit int) string {
	if len(body) <= limit {
		return body
	}
	// Cut on a rune boundary: a split multi-byte rune is invalid UTF-8 and
	// some providers reject it when encoding the transcript.
	for limit > 0 && !utf8.RuneStart(body[limit]) {
		limit--
	}
	return body[:limit] + "\n[truncated]"
}

// navTool binds a definition to a func, since nib ships only the bare
// Tool interface.
type navTool struct {
	def llm.ToolDef
	run func(ctx context.Context, args map[string]any) (string, error)
}

func (t *navTool) Definition() llm.ToolDef { return t.def }

func (t *navTool) Execute(ctx context.Context, call llm.ToolCall) agent.ToolResult {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return agent.ToolResult{Content: "invalid arguments: " + err.Error(), IsError: true}
	}
	out, err := t.run(ctx, args)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}
	}
	return agent.ToolResult{Content: out}
}
