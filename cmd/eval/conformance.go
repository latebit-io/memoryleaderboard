package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type checks struct {
	out    interface{ Write([]byte) (int, error) }
	passed int
	failed int
	warned int
}

func (c *checks) check(name string, condition bool, detail string) {
	if condition {
		c.passed++
		writef(c.out, "PASS %s\n", name)
		return
	}
	c.failed++
	if detail == "" {
		writef(c.out, "FAIL %s\n", name)
	} else {
		writef(c.out, "FAIL %s: %s\n", name, detail)
	}
}

func (c *checks) warn(name, detail string) {
	c.warned++
	writef(c.out, "WARN %s: %s\n", name, detail)
}

func (c *checks) strict(name string, condition, strict bool, detail string) {
	if condition || strict {
		c.check(name, condition, detail)
		return
	}
	c.warn(name, detail+"; use --strict-invalid to fail")
}

func (e evaluator) conformance(apiKey string, strictInvalid bool) int {
	checks := checks{out: e.out}
	run := runID()
	userA := "local-conf-a-" + run
	userB := "local-conf-b-" + run
	const now int64 = 1786838400000

	health := e.client.request("/health", nil)
	healthBody, healthError := decodeResponse(health)
	healthObject, _ := healthBody.(map[string]any)
	checks.check(
		"/health returns healthy JSON",
		health.status == 200 && healthObject != nil && healthObject["status"] == "ok",
		responseDetail(health, healthBody, healthError),
	)

	markerA := "confalpha" + run
	first := addPayload("conf-add-1-"+run, userA, "session-1", markerA+" parser build requires make lint", now)
	added := e.client.request("/add", first)
	addedBody, addedError := decodeResponse(added)
	expectedEcho := map[string]any{
		"success": true, "request_id": first.RequestID, "user_id": userA, "session_id": "session-1",
	}
	checks.check(
		"numeric timestamp accepted and Add echo exact",
		added.status == 200 && reflect.DeepEqual(addedBody, expectedEcho),
		responseDetail(added, addedBody, addedError),
	)

	markerB := "confbravo" + run
	second := addPayload("conf-add-2-"+run, userB, "session-2", markerB+" private deployment uses caddy", now+1)
	secondResponse := e.client.request("/add", second)
	checks.check("cross-user fixture Add succeeds", secondResponse.status == 200, fmt.Sprintf("status=%d", secondResponse.status))

	third := addPayload("conf-add-3-"+run, userA, "session-1", markerA+" secondary parser evidence", now+2)
	thirdResponse := e.client.request("/add", third)
	checks.check("second same-user Add succeeds", thirdResponse.status == 200, fmt.Sprintf("status=%d", thirdResponse.status))

	searchPayload := searchRequest{Query: markerA, Options: []string{"A. parser", "B. deploy"}, UserID: userA, TopK: 100}
	searched := e.client.request("/search", searchPayload)
	searchBody, searchError := decodeResponse(searched)
	var schemaErrors []string
	if searched.status == 200 && searchBody != nil {
		schemaErrors = resultErrors(searchBody, 100)
	} else {
		schemaErrors = []string{firstDetail(searchError, searched.err, fmt.Sprintf("status=%d", searched.status))}
	}
	contents := joinedContents(searchBody)
	checks.check(
		"immediate Search accepts string options and top_k=100",
		searched.status == 200 && contains(contents, markerA),
		responseDetail(searched, searchBody, searchError),
	)
	checks.check(
		"result schema, finite descending scores, and top_k=100 cap",
		searched.status == 200 && len(schemaErrors) == 0,
		joinErrors(schemaErrors, searched.status),
	)

	capped := e.client.request("/search", searchRequest{Query: markerA, UserID: userA, TopK: 1})
	cappedBody, cappedError := decodeResponse(capped)
	var cappedErrors []string
	if capped.status == 200 && cappedBody != nil {
		cappedErrors = resultErrors(cappedBody, 1)
	} else {
		cappedErrors = []string{firstDetail(cappedError, capped.err, fmt.Sprintf("status=%d", capped.status))}
	}
	checks.check(
		"top_k=1 cap",
		capped.status == 200 && len(cappedErrors) == 0,
		firstDetail(cappedError, joinErrors(cappedErrors, capped.status)),
	)

	repeated := e.client.request("/search", searchPayload)
	repeatedBody, repeatedError := decodeResponse(repeated)
	firstIDs := resultIDs(searchBody)
	repeatedIDs := resultIDs(repeatedBody)
	checks.check(
		"stable result IDs and order",
		searched.status == 200 && repeated.status == 200 && reflect.DeepEqual(firstIDs, repeatedIDs),
		firstDetail(repeatedError, fmt.Sprintf("first=%v repeated=%v", firstIDs, repeatedIDs)),
	)

	empty := e.client.request("/search", searchRequest{Query: "nothing", UserID: "empty-" + run, TopK: 100})
	emptyBody, emptyError := decodeResponse(empty)
	checks.check(
		"user with no memories returns empty data array",
		empty.status == 200 && reflect.DeepEqual(emptyBody, map[string]any{"data": []any{}}),
		responseDetail(empty, emptyBody, emptyError),
	)

	isolated := e.client.request("/search", searchRequest{Query: markerB, UserID: userA, TopK: 100})
	isolatedBody, isolatedError := decodeResponse(isolated)
	isolationErrors := resultErrors(isolatedBody, 100)
	leaked := contains(joinedContents(isolatedBody), markerB)
	checks.check(
		"cross-user isolation",
		isolated.status == 200 && len(isolationErrors) == 0 && !leaked,
		firstDetail(isolatedError, strings.Join(isolationErrors, "; "), fmt.Sprintf("status=%d leaked=%t", isolated.status, leaked)),
	)

	if apiKey != "" {
		authPayload := searchRequest{Query: "x", UserID: userA, TopK: 1}
		missing := e.client.requestWithAuth("/search", authPayload, false, "")
		wrong := e.client.requestWithAuth("/search", authPayload, true, apiKey+"-wrong")
		checks.check(
			"configured auth rejects missing and wrong credentials",
			missing.status == 401 && wrong.status == 401,
			fmt.Sprintf("missing=%d wrong=%d", missing.status, wrong.status),
		)
	} else {
		checks.warn("configured auth rejection", "no --api-key supplied; check not applicable")
	}

	stringTimestamp := map[string]any{
		"request_id": "bad-time-" + run,
		"user_id":    userA,
		"session_id": "bad",
		"messages":   []any{map[string]any{"role": "user", "content": "bad", "timestamp": "2026-08-16T00:00:00Z"}},
	}
	invalidCases := []struct {
		name    string
		path    string
		payload any
		raw     []byte
	}{
		{name: "malformed JSON", path: "/add", raw: []byte("{")},
		{name: "string timestamp", path: "/add", payload: stringTimestamp},
		{name: "empty user_id", path: "/search", payload: searchRequest{Query: "x", UserID: "", TopK: 1}},
		{name: "object options", path: "/search", payload: map[string]any{"query": "x", "options": map[string]any{"A": "x"}, "user_id": userA, "top_k": 1}},
		{name: "missing top_k", path: "/search", payload: map[string]any{"query": "x", "user_id": userA}},
		{name: "non-positive top_k", path: "/search", payload: searchRequest{Query: "x", UserID: userA, TopK: 0}},
		{name: "oversized top_k", path: "/search", payload: searchRequest{Query: "x", UserID: userA, TopK: 101}},
	}
	for _, test := range invalidCases {
		var res response
		if test.raw != nil {
			res = e.client.requestRaw(test.path, test.raw, true, "")
		} else {
			res = e.client.request(test.path, test.payload)
		}
		checks.check("invalid payload rejected: "+test.name, res.status == 400, fmt.Sprintf("status=%d", res.status))
	}

	unknownAdd := structMap(addPayload("unknown-"+run, userA, "strict", "strict unknown field probe", now+3))
	unknownAdd["unexpected"] = true
	searchWithUnknown := map[string]any{"query": markerA, "user_id": userA, "top_k": 1, "unexpected": true}
	trailing, _ := json.Marshal(searchRequest{Query: markerA, UserID: userA, TopK: 1})
	strictCases := []struct {
		name string
		res  response
	}{
		{name: "unknown Add field", res: e.client.request("/add", unknownAdd)},
		{name: "unknown Search field", res: e.client.request("/search", searchWithUnknown)},
		{name: "trailing JSON value", res: e.client.requestRaw("/search", append(trailing, []byte(" {}")...), true, "")},
	}
	for _, test := range strictCases {
		checks.strict(
			"strict invalid payload rejected: "+test.name,
			test.res.status == 400,
			strictInvalid,
			fmt.Sprintf("permissive parser returned status=%d", test.res.status),
		)
	}

	writef(e.out, "SUMMARY mode=conformance pass=%d fail=%d warn=%d\n", checks.passed, checks.failed, checks.warned)
	if checks.failed != 0 {
		return 1
	}
	return 0
}

func runID() string {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err == nil {
		return hex.EncodeToString(value)
	}
	digits := fmt.Sprintf("%012x", time.Now().UnixNano())
	return digits[len(digits)-12:]
}

func responseDetail(res response, body any, decodeError string) string {
	if decodeError != "" {
		return decodeError
	}
	if res.err != "" {
		return res.err
	}
	return fmt.Sprintf("status=%d body=%v", res.status, body)
}

func joinedContents(body any) string {
	result := ""
	for _, item := range contentItems(body) {
		if result != "" {
			result += "\n"
		}
		result += item["content"].(string)
	}
	return result
}

func contains(value, substring string) bool {
	return strings.Contains(value, substring)
}

func joinErrors(errors []string, status int) string {
	if len(errors) == 0 {
		return fmt.Sprintf("status=%d", status)
	}
	result := errors[0]
	for _, value := range errors[1:] {
		result += "; " + value
	}
	return result
}

func firstDetail(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func resultIDs(body any) []any {
	items := dataItems(body)
	ids := make([]any, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["id"])
	}
	return ids
}

func structMap(value any) map[string]any {
	raw, _ := json.Marshal(value)
	decoded, _ := decodeJSON(raw)
	return decoded.(map[string]any)
}
