package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

type fakeStore struct {
	added       map[string][]memory.Message
	addErr      error
	records     []memory.Record
	searchQuery string
	searchTopK  int
}

func (f *fakeStore) Add(_ context.Context, userID, sessionID, requestID string, msgs []memory.Message) error {
	if f.addErr != nil {
		return f.addErr
	}
	if f.added == nil {
		f.added = map[string][]memory.Message{}
	}
	f.added[userID+"/"+sessionID+"/"+requestID] = msgs
	return nil
}

func TestAddConflictIsRetryable(t *testing.T) {
	srv := newTestServer(t, &fakeStore{addErr: memory.ErrAddConflict}, "")
	resp := post(t, srv.URL+"/add", `{
		"request_id":"r1","user_id":"u1","session_id":"s1",
		"messages":[{"role":"user","content":"x"}]
	}`, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestAddStoreFailureIsServiceUnavailable(t *testing.T) {
	srv := newTestServer(t, &fakeStore{addErr: errors.New("down")}, "")
	resp := post(t, srv.URL+"/add", `{
		"request_id":"r1","user_id":"u1","session_id":"s1",
		"messages":[{"role":"user","content":"x"}]
	}`, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func (f *fakeStore) Search(_ context.Context, _, query string, topK int) ([]memory.Record, error) {
	f.searchQuery = query
	f.searchTopK = topK
	return f.records, nil
}

func newTestServer(t *testing.T, store memory.Store, apiKey string) *httptest.Server {
	t.Helper()
	e := echo.New()
	Routes(e, NewHandler(store), Config{APIKey: apiKey})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAddEchoesIDs(t *testing.T) {
	store := &fakeStore{}
	srv := newTestServer(t, store, "")

	resp := post(t, srv.URL+"/add", `{
		"request_id": "r1",
		"user_id": "u1",
		"session_id": "s1",
		"messages": [{"role": "user", "content": "remember the build uses make", "timestamp": 1704067200000}]
	}`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out AddResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Success || out.RequestID != "r1" || out.UserID != "u1" || out.SessionID != "s1" {
		t.Fatalf("bad echo: %+v", out)
	}
	messages := store.added["u1/s1/r1"]
	if len(messages) != 1 {
		t.Fatalf("store not called: %+v", store.added)
	}
	if messages[0].Timestamp == nil || *messages[0].Timestamp != 1704067200000 {
		t.Fatalf("timestamp = %v, want Unix milliseconds", messages[0].Timestamp)
	}
}

func TestAddValidates(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "")
	for name, body := range map[string]string{
		"missing fields":   `{"request_id":"r1"}`,
		"missing session":  `{"request_id":"r1","user_id":"u1","messages":[{"role":"user","content":"x"}]}`,
		"empty role":       `{"request_id":"r1","user_id":"u1","session_id":"s1","messages":[{"role":"","content":"x"}]}`,
		"empty content":    `{"request_id":"r1","user_id":"u1","session_id":"s1","messages":[{"role":"user","content":""}]}`,
		"string timestamp": `{"request_id":"r1","user_id":"u1","session_id":"s1","messages":[{"role":"user","content":"x","timestamp":"2026-01-01T00:00:00Z"}]}`,
	} {
		resp := post(t, srv.URL+"/add", body, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestSearchOrderedData(t *testing.T) {
	store := &fakeStore{records: []memory.Record{
		{ID: "a", Content: "top", Score: 0.9},
		{ID: "b", Content: "second", Score: 0.5},
	}}
	srv := newTestServer(t, store, "")

	resp := post(t, srv.URL+"/search", `{"query": "build", "user_id": "u1", "top_k": 1}`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].ID != "a" {
		t.Fatalf("bad data: %+v", out.Data)
	}
	if store.searchTopK != 1 {
		t.Fatalf("store topK = %d, want 1", store.searchTopK)
	}
}

func TestSearchEmptyIsArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "")
	resp := post(t, srv.URL+"/search", `{"query": "q", "user_id": "u1", "top_k": 100}`, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["data"]) != "[]" {
		t.Fatalf("data = %s, want []", raw["data"])
	}
}

func TestSearchContractFields(t *testing.T) {
	store := &fakeStore{}
	srv := newTestServer(t, store, "")

	resp := post(t, srv.URL+"/search", `{"query":"q","options":["A. one","B. two"],"user_id":"u1","top_k":100}`, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("array options status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"q", "A. one", "B. two"} {
		if !strings.Contains(store.searchQuery, want) {
			t.Errorf("search query %q missing %q", store.searchQuery, want)
		}
	}

	for name, body := range map[string]string{
		"object options": `{"query":"q","options":{"a":"one"},"user_id":"u1","top_k":100}`,
		"missing top_k":  `{"query":"q","user_id":"u1"}`,
		"zero top_k":     `{"query":"q","user_id":"u1","top_k":0}`,
		"negative top_k": `{"query":"q","user_id":"u1","top_k":-1}`,
		"empty option":   `{"query":"q","options":[""],"user_id":"u1","top_k":100}`,
	} {
		resp := post(t, srv.URL+"/search", body, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestHealthPathsAreUnauthenticated(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "sekrit")
	for _, path := range []string{"/health", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestAuthSchemes(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "sekrit")
	body := `{"query": "q", "user_id": "u1", "top_k": 100}`

	for name, headers := range map[string]map[string]string{
		"bearer": {"Authorization": "Bearer sekrit"},
		"token":  {"Authorization": "Token sekrit"},
		"apikey": {"X-Api-Key": "sekrit"},
	} {
		resp := post(t, srv.URL+"/search", body, headers)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, resp.StatusCode)
		}
	}

	resp := post(t, srv.URL+"/search", body, map[string]string{"X-Api-Key": "wrong"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", resp.StatusCode)
	}
}
