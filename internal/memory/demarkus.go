package memory

import (
	"context"
	"encoding/base32"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/demarkus/protocol"
)

// fetchConcurrency bounds the parallel per-hit fetch fan-out in Search.
const fetchConcurrency = 16

// DemarkusStore stores each Add request as a document under the user's
// subtree and answers Search via catalog lookup + fetch. Phase 1: naive
// tagging and lookup-only ranking; the distill/nav agents replace both.
type DemarkusStore struct {
	client *fetch.Client
	host   string
	token  string
}

// NewDemarkusStore connects to a demarkus server, e.g. host "localhost:6309".
func NewDemarkusStore(host, token string, insecure bool) *DemarkusStore {
	return &DemarkusStore{
		client: fetch.NewClient(fetch.Options{Insecure: insecure}),
		host:   host,
		token:  token,
	}
}

// Close releases pooled connections.
func (s *DemarkusStore) Close() { s.client.Close() }

func userPrefix(userID string) string {
	return "/u/" + sanitize(userID)
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
	return fmt.Sprintf("%s/sessions/%s/%s.md", userPrefix(userID), sanitize(sessionID), sanitize(requestID))
}

// Add publishes the conversation verbatim. Unconditional publish keeps
// harness retries of the same request_id idempotent.
func (s *DemarkusStore) Add(_ context.Context, userID, sessionID, requestID string, msgs []Message) error {
	title := fmt.Sprintf("Session %s: %s", sessionID, requestID)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	for _, m := range msgs {
		if m.Timestamp != "" {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n", m.Timestamp, m.Role, m.Content)
		} else {
			fmt.Fprintf(&b, "- **%s**: %s\n", m.Role, m.Content)
		}
	}
	meta := map[string]string{
		"title":      title,
		"tags":       strings.Join(naiveTags(msgs), ","),
		"importance": "0.5",
	}
	path := s.docPath(userID, sessionID, requestID)
	res, err := s.client.Publish(s.host, path, b.String(), s.token, -1, meta)
	if err != nil {
		return err
	}
	if st := res.Response.Status; st != protocol.StatusOK && st != protocol.StatusCreated {
		return fmt.Errorf("publish %s: status %s", path, st)
	}
	return nil
}

// Search ranks via catalog lookup under the user's prefix, then fetches
// each hit's body as the evidence content.
func (s *DemarkusStore) Search(ctx context.Context, userID, query string, topK int) ([]Record, error) {
	res, err := s.client.Lookup(s.host, userPrefix(userID)+"/", query, s.token, fetch.LookupOptions{Limit: topK})
	if err != nil {
		return nil, err
	}
	// A user with no memories yet has no scope; that is an empty result,
	// not an outage.
	if st := res.Response.Status; st == protocol.StatusNotFound {
		return nil, nil
	} else if st != protocol.StatusOK {
		return nil, fmt.Errorf("lookup %s: status %s", userPrefix(userID), st)
	}
	hits := parseLookupTable(res.Response.Body)

	// Fan out fetches: QUIC multiplexes streams on the pooled connection,
	// and at top_k=100 serial round-trips would dominate search latency.
	// Index-addressed results preserve the server's rank order.
	results := make([]*Record, len(hits))
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
			doc, err := s.client.Fetch(s.host, h.path, s.token)
			if err != nil {
				errs[i] = err
				return
			}
			if doc.Response.Status != protocol.StatusOK {
				errs[i] = fmt.Errorf("fetch %s: status %s", h.path, doc.Response.Status)
				return
			}
			results[i] = &Record{
				ID:        h.path,
				Content:   doc.Response.Body,
				Score:     h.importance,
				CreatedAt: doc.Response.Metadata["modified"],
			}
		}(i, h)
	}
	wg.Wait()

	records := make([]Record, 0, len(hits))
	var fetchErr error
	for i, r := range results {
		if r != nil {
			records = append(records, *r)
		} else if errs[i] != nil {
			fetchErr = errs[i]
		}
	}
	// Partial evidence still scores; fail only when every fetch failed.
	if len(records) == 0 && fetchErr != nil {
		return nil, fetchErr
	}
	return records, nil
}

type lookupHit struct {
	path       string
	importance float64
}

// parseLookupTable extracts (path, importance) rows from the markdown
// table a LOOKUP returns, preserving server rank order.
func parseLookupTable(body string) []lookupHit {
	var hits []lookupHit
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| /") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		path := strings.TrimSpace(cells[1])
		imp, _ := strconv.ParseFloat(strings.TrimSpace(cells[2]), 64)
		hits = append(hits, lookupHit{path: path, importance: imp})
	}
	return hits
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true,
	"with": true, "you": true, "your": true, "have": true, "from": true,
	"was": true, "were": true, "are": true, "not": true, "but": true,
	"what": true, "when": true, "where": true, "how": true, "why": true,
	"can": true, "could": true, "would": true, "should": true, "about": true,
}

var wordRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_-]{2,}`)

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
