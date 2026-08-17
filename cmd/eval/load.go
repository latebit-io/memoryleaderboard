package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var loadMarker = regexp.MustCompile(`\[\[AML_LOAD:(\d+):(\d+)\]\]`)

type loadConfig struct {
	adds        int
	users       int
	searches    int
	concurrency int
	topK        int
}

type phaseResult struct {
	status     int
	latency    float64
	schema     bool
	isolation  bool
	visibility bool
}

func (e evaluator) load(cfg loadConfig) int {
	if cfg.adds < 1 || cfg.searches < 1 || cfg.concurrency < 1 || cfg.users < 0 {
		writeLine(e.err, "ERROR --adds, --searches, and --concurrency must be positive; --users cannot be negative")
		return 2
	}
	userCount := cfg.users
	if userCount == 0 {
		userCount = cfg.adds
	}
	if userCount > cfg.adds {
		writeLine(e.err, "ERROR --users cannot exceed --adds")
		return 2
	}
	run := runID()
	users := make([]string, userCount)
	for index := range users {
		users[index] = fmt.Sprintf("local-load-%s-%d", run, index)
	}

	addResults := concurrentPhase(cfg.adds, cfg.concurrency, func(index int) phaseResult {
		userIndex := index % userCount
		payload := addPayload(
			fmt.Sprintf("load-add-%s-%d", run, index),
			users[userIndex],
			"load-session",
			fmt.Sprintf("load shared retrieval phrase [[AML_LOAD:%d:%d]]", userIndex, index),
			1786838400000+int64(index),
		)
		res := e.client.request("/add", payload)
		body, _ := decodeResponse(res)
		expected := map[string]any{
			"success": true, "request_id": payload.RequestID, "user_id": users[userIndex], "session_id": "load-session",
		}
		return phaseResult{status: res.status, latency: res.elapsedMS, schema: mapsEqual(body, expected)}
	})
	printPhase(e.out, "add", addResults)

	searchResults := concurrentPhase(cfg.searches, cfg.concurrency, func(operation int) phaseResult {
		userIndex := operation % userCount
		res := e.client.request("/search", searchRequest{Query: "load shared retrieval phrase", UserID: users[userIndex], TopK: cfg.topK})
		body, bodyError := decodeResponse(res)
		var errors []string
		if res.status == 200 && body != nil {
			errors = resultErrors(body, cfg.topK)
		} else {
			errors = []string{firstDetail(bodyError, res.err, "HTTP failure")}
		}
		leak := false
		visible := make(map[int]bool)
		for _, result := range contentItems(body) {
			for _, match := range loadMarker.FindAllStringSubmatch(result["content"].(string), -1) {
				markerUser, userErr := parseDecimal(match[1])
				markerRecord, recordErr := parseDecimal(match[2])
				if userErr != nil || recordErr != nil {
					continue
				}
				if markerUser != userIndex {
					leak = true
				} else {
					visible[markerRecord] = true
				}
			}
		}
		expected := min(recordsForUser(cfg.adds, userCount, userIndex), cfg.topK)
		return phaseResult{
			status: res.status, latency: res.elapsedMS, schema: len(errors) == 0,
			isolation: !leak, visibility: len(visible) >= expected,
		}
	})
	printPhase(e.out, "search", searchResults)

	addHTTPFailures := countResults(addResults, func(result phaseResult) bool { return result.status != 200 })
	addSchemaFailures := countResults(addResults, func(result phaseResult) bool { return result.status == 200 && !result.schema })
	searchHTTPFailures := countResults(searchResults, func(result phaseResult) bool { return result.status != 200 })
	searchSchemaFailures := countResults(searchResults, func(result phaseResult) bool { return result.status == 200 && !result.schema })
	isolationViolations := countResults(searchResults, func(result phaseResult) bool { return !result.isolation })
	visibilityFailures := countResults(searchResults, func(result phaseResult) bool { return !result.visibility })
	failures := addHTTPFailures + addSchemaFailures + searchHTTPFailures + searchSchemaFailures + isolationViolations + visibilityFailures
	writef(
		e.out,
		"SUMMARY mode=load add_http_failures=%d add_schema_failures=%d search_http_failures=%d search_schema_failures=%d isolation_violations=%d visibility_failures=%d\n",
		addHTTPFailures, addSchemaFailures, searchHTTPFailures, searchSchemaFailures, isolationViolations, visibilityFailures,
	)
	if failures != 0 {
		return 1
	}
	return 0
}

func concurrentPhase(count, concurrency int, operation func(int) phaseResult) []phaseResult {
	results := make([]phaseResult, count)
	work := make(chan int)
	done := make(chan struct{}, concurrency)
	for range min(count, concurrency) {
		go func() {
			for index := range work {
				results[index] = operation(index)
			}
			done <- struct{}{}
		}()
	}
	for index := range count {
		work <- index
	}
	close(work)
	for range min(count, concurrency) {
		<-done
	}
	return results
}

func printPhase(out interface{ Write([]byte) (int, error) }, name string, results []phaseResult) {
	statuses := make(map[int]int)
	latencies := make([]float64, 0, len(results))
	for _, result := range results {
		statuses[result.status]++
		latencies = append(latencies, result.latency)
	}
	statusCodes := make([]int, 0, len(statuses))
	for status := range statuses {
		statusCodes = append(statusCodes, status)
	}
	sort.Ints(statusCodes)
	statusParts := make([]string, 0, len(statusCodes))
	for _, status := range statusCodes {
		statusParts = append(statusParts, fmt.Sprintf("%d:%d", status, statuses[status]))
	}
	writef(
		out,
		"PHASE %s count=%d statuses=%s mean_ms=%.1f p50_ms=%.1f p95_ms=%.1f p99_ms=%.1f max_ms=%.1f\n",
		name, len(results), strings.Join(statusParts, ","), mean(latencies), percentile(latencies, .50), percentile(latencies, .95),
		percentile(latencies, .99), maxFloat(latencies),
	)
}

func countResults(results []phaseResult, predicate func(phaseResult) bool) int {
	count := 0
	for _, result := range results {
		if predicate(result) {
			count++
		}
	}
	return count
}

func recordsForUser(adds, users, user int) int {
	count := 0
	for index := range adds {
		if index%users == user {
			count++
		}
	}
	return count
}

func maxFloat(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func mapsEqual(value any, expected map[string]any) bool {
	actual, ok := value.(map[string]any)
	if !ok || len(actual) != len(expected) {
		return false
	}
	for key, expectedValue := range expected {
		if actual[key] != expectedValue {
			return false
		}
	}
	return true
}

func parseDecimal(value string) (int, error) {
	return strconv.Atoi(value)
}
