package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus/protocol"
)

// fetchConcurrency bounds the parallel per-hit fetch fan-out in Search.
const fetchConcurrency = 16

// DemarkusStore stores each Add request as a document under the user's
// subtree and answers Search via catalog lookup + fetch. Phase 1: naive
// tagging and lookup-only ranking; the distill/nav agents replace both.
// It also implements [Navigator] for agentic strategies.
type DemarkusStore struct {
	reader    *markReadClient
	host      string
	token     string
	distiller Distiller
}

// NewDemarkusStore connects to a demarkus server, e.g. host "localhost:6309".
func NewDemarkusStore(host, token string, insecure bool) *DemarkusStore {
	return &DemarkusStore{
		reader: newMarkReadClient(insecure),
		host:   host,
		token:  token,
	}
}

// Close releases pooled connections.
func (s *DemarkusStore) Close() {
	s.reader.Close()
}

func (s *DemarkusStore) SetDistiller(distiller Distiller) {
	s.distiller = distiller
}

func (s *DemarkusStore) DistillationEnabled() bool {
	return s.distiller != nil
}

// Scope returns the root path of a user's documents.
func (s *DemarkusStore) Scope(userID string) string {
	return "/u/" + sanitize(userID)
}

// Lookup matches a subject against the catalog under scope. An absent
// scope (a user with no documents yet) returns an empty table, not an
// error: it is a normal state, not an outage.
func (s *DemarkusStore) Lookup(ctx context.Context, scope, query string, limit int) (string, error) {
	meta := map[string]string{"query": query}
	if limit > 0 {
		meta["limit"] = strconv.Itoa(limit)
	}
	if s.token != "" {
		meta["auth"] = s.token
	}
	res, err := s.reader.Request(ctx, s.host, protocol.Request{Verb: protocol.VerbLookup, Path: scope, Metadata: meta})
	if err != nil {
		return "", err
	}
	switch res.Status {
	case protocol.StatusNotFound:
		return "", nil
	case protocol.StatusOK:
		return res.Body, nil
	default:
		return "", fmt.Errorf("lookup %s: status %s", scope, res.Status)
	}
}

// List returns a directory listing.
func (s *DemarkusStore) List(ctx context.Context, path string) (string, error) {
	res, err := s.reader.Request(ctx, s.host, s.readRequest(protocol.VerbList, path))
	if err != nil {
		return "", err
	}
	if res.Status != protocol.StatusOK {
		return "", fmt.Errorf("list %s: status %s", path, res.Status)
	}
	return res.Body, nil
}

// FetchRecord reads one document as an evidence record.
func (s *DemarkusStore) FetchRecord(ctx context.Context, path string) (Record, error) {
	res, err := s.reader.Request(ctx, s.host, s.readRequest(protocol.VerbFetch, path))
	if err != nil {
		return Record{}, err
	}
	if res.Status != protocol.StatusOK {
		return Record{}, fmt.Errorf("fetch %s: status %s", path, res.Status)
	}
	return Record{
		ID:        path,
		Content:   res.Body,
		CreatedAt: firstNonempty(res.Metadata["source-end"], res.Metadata["modified"]),
	}, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *DemarkusStore) readRequest(verb, path string) protocol.Request {
	meta := make(map[string]string)
	if s.token != "" {
		meta["auth"] = s.token
	}
	return protocol.Request{Verb: verb, Path: path, Metadata: meta}
}

// Lowercase base32 keeps the encoding injective on case-insensitive
// filesystems (macOS default); base64url would collide on letter case.
var pathEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// sanitize maps ids to path segments injectively; user_id is the
// isolation boundary, so distinct ids must never share a subtree.
func sanitize(id string) string {
	return pathEncoding.EncodeToString([]byte(id))
}

func (s *DemarkusStore) docPath(userID, sessionID, requestID string) string {
	return fmt.Sprintf("%s/sessions/%s/%s.md", s.Scope(userID), sanitize(sessionID), sanitize(requestID))
}

var ErrAddConflict = errors.New("request_id already contains different memory")

// Add stores distilled retrieval metadata alongside lossless raw messages.
func (s *DemarkusStore) Add(ctx context.Context, userID, sessionID, requestID string, msgs []Message) error {
	path := s.docPath(userID, sessionID, requestID)
	legacyBody := renderLegacyMemory(sessionID, requestID, msgs)
	sourceHash, err := messageHash(msgs)
	if err != nil {
		return err
	}
	idempotent, err := s.checkExisting(ctx, path, sourceHash, legacyBody)
	if err != nil || idempotent {
		return err
	}

	distilled := fallbackDistillation(msgs, sessionID, requestID)
	if s.distiller != nil {
		candidate, distillErr := s.distiller.Distill(ctx, msgs)
		if distillErr != nil {
			slog.Warn("memory distillation failed; using deterministic metadata", "request_id", requestID, "err", distillErr)
		} else {
			distilled = candidate
		}
	}

	meta := map[string]string{
		"title":       distilled.Title,
		"tags":        strings.Join(distilled.Tags, ","),
		"importance":  strconv.FormatFloat(distilled.Importance, 'f', -1, 64),
		"source-hash": sourceHash,
	}
	if start, end, ok := sourceTimes(msgs); ok {
		meta["source-start"] = start
		meta["source-end"] = end
	}
	requestMeta := make(map[string]string, len(meta)+2)
	for key, value := range meta {
		requestMeta[key] = value
	}
	requestMeta["expected-version"] = "0"
	if s.token != "" {
		requestMeta["auth"] = s.token
	}
	res, err := s.reader.Request(ctx, s.host, protocol.Request{
		Verb: protocol.VerbPublish, Path: path, Metadata: requestMeta,
		Body: renderMemory(distilled, msgs),
	})
	if err != nil {
		return err
	}
	if res.Status == protocol.StatusConflict {
		idempotent, checkErr := s.checkExisting(ctx, path, sourceHash, legacyBody)
		if checkErr != nil || idempotent {
			return checkErr
		}
	}
	if res.Status != protocol.StatusOK && res.Status != protocol.StatusCreated {
		return fmt.Errorf("publish %s: status %s", path, res.Status)
	}
	return nil
}

func (s *DemarkusStore) checkExisting(ctx context.Context, path, sourceHash, legacyBody string) (bool, error) {
	res, err := s.reader.Request(ctx, s.host, s.readRequest(protocol.VerbFetch, path))
	if err != nil {
		return false, err
	}
	switch res.Status {
	case protocol.StatusNotFound:
		return false, nil
	case protocol.StatusOK:
		if res.Metadata["source-hash"] == sourceHash ||
			(res.Metadata["source-hash"] == "" && res.Body == legacyBody) {
			return true, nil
		}
		return false, fmt.Errorf("%w: %s", ErrAddConflict, path)
	default:
		return false, fmt.Errorf("fetch %s before publish: status %s", path, res.Status)
	}
}

func renderLegacyMemory(sessionID, requestID string, msgs []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s: %s\n\n", sessionID, requestID)
	for _, message := range msgs {
		if message.Timestamp != nil {
			fmt.Fprintf(&b, "- [%d] **%s**: %s\n", *message.Timestamp, message.Role, message.Content)
		} else {
			fmt.Fprintf(&b, "- **%s**: %s\n", message.Role, message.Content)
		}
	}
	return b.String()
}

func messageHash(msgs []Message) (string, error) {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return "", fmt.Errorf("marshal source messages: %w", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256-%x", sum), nil
}

func sourceTimes(msgs []Message) (string, string, bool) {
	var minTime, maxTime int64
	found := false
	for _, message := range msgs {
		if message.Timestamp == nil {
			continue
		}
		value := *message.Timestamp
		if !found || value < minTime {
			minTime = value
		}
		if !found || value > maxTime {
			maxTime = value
		}
		found = true
	}
	if !found {
		return "", "", false
	}
	return time.UnixMilli(minTime).UTC().Format(time.RFC3339Nano),
		time.UnixMilli(maxTime).UTC().Format(time.RFC3339Nano), true
}

func fallbackDistillation(msgs []Message, sessionID, requestID string) Distillation {
	title := fmt.Sprintf("Session %s: %s", sessionID, requestID)
	for _, message := range msgs {
		if content := strings.Join(strings.Fields(message.Content), " "); content != "" {
			runes := []rune(content)
			if len(runes) > 96 {
				content = string(runes[:96]) + "..."
			}
			title = content
			break
		}
	}
	return Distillation{Title: title, Tags: naiveTags(msgs), Importance: 0.5}
}

func renderMemory(distilled Distillation, msgs []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", distilled.Title)
	if distilled.Summary != "" {
		fmt.Fprintf(&b, "## Distilled memory\n\n%s\n\n", distilled.Summary)
	}
	b.WriteString("## Raw messages\n\n")
	for _, m := range msgs {
		if m.Timestamp != nil {
			stamp := time.UnixMilli(*m.Timestamp).UTC().Format(time.RFC3339Nano)
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n", stamp, m.Role, m.Content)
		} else {
			fmt.Fprintf(&b, "- **%s**: %s\n", m.Role, m.Content)
		}
	}
	return b.String()
}

// Search ranks via catalog lookup under the user's prefix, then fetches
// each hit's body as the evidence content.
func (s *DemarkusStore) Search(ctx context.Context, userID, query string, topK int) ([]Record, error) {
	scope := s.Scope(userID)
	table, err := s.Lookup(ctx, scope+"/", query, topK)
	if err != nil {
		return nil, err
	}
	hits := parseLookupTable(table)

	// Fan out fetches: QUIC multiplexes streams on the pooled connection,
	// and at top_k=100 serial round-trips would dominate search latency.
	// Index-addressed results preserve the server's rank order.
	records := make([]Record, len(hits))
	found := make([]bool, len(hits))
	errs := make([]error, len(hits))
	sem := make(chan struct{}, fetchConcurrency)
	var wg sync.WaitGroup
	for i, h := range hits {
		if ctx.Err() != nil {
			break // caller gone; stop burning streams
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, h lookupHit) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, err := s.FetchRecord(ctx, h.path)
			if err != nil {
				errs[i] = err
				return
			}
			records[i], found[i] = rec, true
		}(i, h)
	}
	wg.Wait()

	// Compact in place; writes never run ahead of the read index.
	out := records[:0]
	var fetchErr error
	for i := range records {
		if found[i] {
			out = append(out, records[i])
		} else if errs[i] != nil {
			fetchErr = errs[i]
		}
	}
	// Partial evidence still scores; fail only when every fetch failed.
	if len(out) == 0 && fetchErr != nil {
		return nil, fetchErr
	}
	return withRankScores(out), nil
}

type lookupHit struct {
	path string
}

// parseLookupTable extracts paths from the markdown table a LOOKUP returns,
// preserving server rank order.
func parseLookupTable(body string) []lookupHit {
	var hits []lookupHit
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| /") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		path := strings.TrimSpace(cells[1])
		hits = append(hits, lookupHit{path: path})
	}
	return hits
}

// stopWords covers both length classes the tokenizer admits: dropping the
// two-letter function words matters as much as the longer ones now that
// two-character tokens are tagged.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true,
	"with": true, "you": true, "your": true, "have": true, "from": true,
	"was": true, "were": true, "are": true, "not": true, "but": true,
	"what": true, "when": true, "where": true, "how": true, "why": true,
	"can": true, "could": true, "would": true, "should": true, "about": true,
	"is": true, "it": true, "of": true, "to": true, "in": true, "on": true,
	"we": true, "my": true, "by": true, "so": true, "as": true, "at": true,
	"an": true, "be": true, "do": true, "if": true, "or": true, "us": true,
}

// Two-character minimum: acronyms (CI, DB, ML) carry as much retrieval
// signal as long words and are what a query most often names.
var wordRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_-]+`)

// naiveTags picks the most frequent non-stopword terms so lookup has
// something to match. Placeholder for the Add-time distillation cascade.
func naiveTags(msgs []Message) []string {
	freq := map[string]int{}
	for _, m := range msgs {
		for _, w := range wordRe.FindAllString(strings.ToLower(m.Content), -1) {
			if !stopWords[w] {
				freq[w]++
			}
		}
	}
	words := make([]string, 0, len(freq))
	for w := range freq {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool {
		if freq[words[i]] != freq[words[j]] {
			return freq[words[i]] > freq[words[j]]
		}
		return words[i] < words[j]
	})
	if len(words) > 8 {
		words = words[:8]
	}
	if len(words) == 0 {
		words = []string{"conversation"}
	}
	return words
}
