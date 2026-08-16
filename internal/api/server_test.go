package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

type fakeStore struct {
	added   map[string][]memory.Message
	records []memory.Record
}

func (f *fakeStore) Add(_ context.Context, userID, sessionID, requestID string, msgs []memory.Message) error {
	if f.added == nil {
		f.added = map[string][]memory.Message{}
	}
	f.added[userID+"/"+sessionID+"/"+requestID] = msgs
	return nil
}

func (f *fakeStore) Search(_ context.Context, _, _ string, topK int) ([]memory.Record, error) {
	if len(f.records) > topK {
		return f.records[:topK], nil
	}
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
		"messages": [{"role": "user", "content": "remember the build uses make"}]
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
	if len(store.added["u1/s1/r1"]) != 1 {
		t.Fatalf("store not called: %+v", store.added)
	}
}

func TestAddValidates(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "")
	resp := post(t, srv.URL+"/add", `{"request_id": "r1"}`, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
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
}

func TestSearchEmptyIsArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "")
	resp := post(t, srv.URL+"/search", `{"query": "q", "user_id": "u1"}`, nil)
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

func TestAuthSchemes(t *testing.T) {
	srv := newTestServer(t, &fakeStore{}, "sekrit")
	body := `{"query": "q", "user_id": "u1"}`

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
