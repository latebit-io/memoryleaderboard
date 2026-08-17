package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMalformedJSONAndData(t *testing.T) {
	if _, err := decodeJSON([]byte(`{"data":[]} {}`)); err == nil {
		t.Fatal("decodeJSON accepted trailing JSON")
	}
	body, err := decodeJSON([]byte(`{"data":[null,{"id":"x","content":null,"score":"bad"},{"id":"x","content":"valid","score":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	errors := resultErrors(body, 5)
	if len(errors) < 3 {
		t.Fatalf("resultErrors returned too few errors: %v", errors)
	}
	items := contentItems(body)
	if len(items) != 1 || items[0]["content"] != "valid" {
		t.Fatalf("contentItems = %#v", items)
	}
}

func TestMetricCutoffLabelMixed(t *testing.T) {
	three := 3
	queries := []fixtureQuery{{}, {TopK: &three}}
	if got := metricCutoffLabel(queries, 5); got != "mixed" {
		t.Fatalf("metricCutoffLabel = %q, want mixed", got)
	}
	if got := metricCutoffLabel([]fixtureQuery{{}, {}}, 5); got != "5" {
		t.Fatalf("metricCutoffLabel = %q, want 5", got)
	}
}

func TestFixtureValidation(t *testing.T) {
	raw, err := os.ReadFile("../../eval/fixtures/synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if errors := validateFixture(value); len(errors) != 0 {
		t.Fatalf("valid fixture errors: %v", errors)
	}

	root := value.(map[string]any)
	queries := root["queries"].([]any)
	first := queries[0].(map[string]any)
	first["top_k"] = true
	first["relevant"] = []any{"missing-record"}
	errors := strings.Join(validateFixture(root), "\n")
	for _, expected := range []string{"top_k must be an integer between 1 and 100", "references unknown record"} {
		if !strings.Contains(errors, expected) {
			t.Errorf("validation errors missing %q: %s", expected, errors)
		}
	}
}

func TestPrefixes(t *testing.T) {
	input := `{"data":[{"id":"/u/bravo/a.md","content":"a","score":1},{"id":"/u/alpha/b.md","content":"b","score":0.5},{"id":"/u/bravo/c.md","content":"c","score":0.3}]}HTTPSTATUS:200`
	var output, errors bytes.Buffer
	if code := runPrefixes(strings.NewReader(input), &output, &errors); code != 0 {
		t.Fatalf("runPrefixes code = %d", code)
	}
	if got, want := output.String(), "/u/alpha /u/bravo\n"; got != want {
		t.Fatalf("runPrefixes output = %q, want %q", got, want)
	}

	output.Reset()
	errors.Reset()
	if code := runPrefixes(strings.NewReader("not-jsonHTTPSTATUS:500"), &output, &errors); code != 1 {
		t.Fatalf("malformed code = %d, want 1", code)
	}
	if output.String() != "" || !strings.Contains(errors.String(), "invalid JSON") {
		t.Fatalf("malformed stdout = %q, stderr = %q", output.String(), errors.String())
	}
}

func TestDecodeJSONRejectsInvalidUTF8(t *testing.T) {
	if _, err := decodeJSON([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}); err == nil {
		t.Fatal("decodeJSON accepted invalid UTF-8")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type delayedErrorBody struct {
	delay time.Duration
}

func (body delayedErrorBody) Read([]byte) (int, error) {
	time.Sleep(body.delay)
	return 0, errors.New("read failed")
}

func (delayedErrorBody) Close() error { return nil }

func TestClientReadFailureIsNotHTTP200(t *testing.T) {
	client := &client{
		baseURL: "http://example.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: delayedErrorBody{delay: 10 * time.Millisecond}}, nil
		})},
	}
	result := client.request("/health", nil)
	if result.status != 0 || !strings.Contains(result.err, "read failed") {
		t.Fatalf("response = %+v", result)
	}
	if result.elapsedMS < 9 {
		t.Fatalf("elapsed = %.1fms, want body-read time included", result.elapsedMS)
	}
}

func TestClientOversizedResponseIsNotHTTP200(t *testing.T) {
	client := &client{
		baseURL: "http://example.test",
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := io.NopCloser(bytes.NewReader(make([]byte, maxResponseBytes+1)))
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		})},
	}
	result := client.request("/health", nil)
	if result.status != 0 || !strings.Contains(result.err, "response exceeds") {
		t.Fatalf("response = %+v", result)
	}
}

func TestTimeoutValidation(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "0.0000000001", "1e100", "86401"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--timeout", value, "conformance"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr); code != 2 {
			t.Errorf("timeout %s: code = %d, want 2", value, code)
		}
	}
}

func TestPrefixesRejectUnscopedID(t *testing.T) {
	input := `{"data":[{"id":"relative.md","content":"x","score":1}]}`
	var output, errors bytes.Buffer
	if code := runPrefixes(strings.NewReader(input), &output, &errors); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errors.String(), "invalid scoped record id") {
		t.Fatalf("stderr = %q", errors.String())
	}
}

func TestNavCheck(t *testing.T) {
	input := `{"data":[{"id":"/u/u/ci.md","content":"flaky CI was fixed with TZ=UTC\nmore","score":0.9}]}HTTPSTATUS:200`
	var output, errors bytes.Buffer
	if code := runNavCheck(strings.NewReader(input), &output, &errors); code != 0 {
		t.Fatalf("runNavCheck code = %d, stderr = %s", code, errors.String())
	}
	for _, expected := range []string{"1 records", "/u/u/ci.md score=0.9 flaky CI was fixed", "relevant=1/2 noise=0"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output missing %q: %s", expected, output.String())
		}
	}

	output.Reset()
	errors.Reset()
	noisy := `{"data":[{"id":"/u/u/deploy.md","content":"caddy deployment","score":1}]}`
	if code := runNavCheck(strings.NewReader(noisy), &output, &errors); code != 1 {
		t.Fatalf("noisy runNavCheck code = %d, want 1", code)
	}
}
