package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	scenarios       = map[string]bool{"semantic": true, "temporal": true, "multi-hop": true, "distractor": true, "negative": true, "isolation": true}
)

type fixture struct {
	Version int
	Name    string
	Records []fixtureRecord
	Queries []fixtureQuery
}

type fixtureRecord struct {
	ID        string
	UserID    string
	SessionID string
	Timestamp int64
	Content   string
}

type fixtureQuery struct {
	ID       string
	UserID   string
	Scenario string
	Query    string
	Relevant []string
	Negative []string
	TopK     *int
}

func loadFixture(path string) (fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fixture{}, fmt.Errorf("cannot read fixture %s: %w", path, err)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return fixture{}, fmt.Errorf("cannot read fixture %s: %w", path, err)
	}
	errors := validateFixture(value)
	if len(errors) != 0 {
		return fixture{}, fmt.Errorf("invalid fixture:\n  %s", strings.Join(errors, "\n  "))
	}
	return fixtureFromValue(value), nil
}

func validateFixture(value any) []string {
	root, ok := value.(map[string]any)
	if !ok {
		return []string{"root must be an object"}
	}
	errors := make([]string, 0)
	version, versionOK := integer(root["version"])
	if !versionOK || version != 1 {
		errors = append(errors, "version must be 1")
	}
	if extra := unknownKeys(root, map[string]bool{"version": true, "name": true, "records": true, "queries": true}); len(extra) != 0 {
		errors = append(errors, fmt.Sprintf("root has unknown fields: %v", extra))
	}
	if name, ok := root["name"].(string); !ok || name == "" {
		errors = append(errors, "name must be a non-empty string")
	}
	records, recordsOK := root["records"].([]any)
	if !recordsOK || len(records) == 0 {
		errors = append(errors, "records must be a non-empty array")
		records = nil
	}
	queries, queriesOK := root["queries"].([]any)
	if !queriesOK || len(queries) == 0 {
		errors = append(errors, "queries must be a non-empty array")
		queries = nil
	}

	recordIDs := make(map[string]bool)
	recordUsers := make(map[string]string)
	for index, value := range records {
		prefix := fmt.Sprintf("records[%d]", index)
		record, ok := value.(map[string]any)
		if !ok {
			errors = append(errors, prefix+" must be an object")
			continue
		}
		if extra := unknownKeys(record, map[string]bool{"id": true, "user_id": true, "session_id": true, "timestamp": true, "content": true}); len(extra) != 0 {
			errors = append(errors, fmt.Sprintf("%s has unknown fields: %v", prefix, extra))
		}
		for _, key := range []string{"id", "user_id", "session_id", "content"} {
			if text, ok := record[key].(string); !ok || text == "" {
				errors = append(errors, fmt.Sprintf("%s.%s must be a non-empty string", prefix, key))
			}
		}
		if id, ok := record["id"].(string); ok {
			if !sourceIDPattern.MatchString(id) {
				errors = append(errors, prefix+".id contains unsupported marker characters")
			}
			if recordIDs[id] {
				errors = append(errors, fmt.Sprintf("duplicate record id %q", id))
			}
			recordIDs[id] = true
			recordUsers[id], _ = record["user_id"].(string)
		}
		if _, ok := integer(record["timestamp"]); !ok {
			errors = append(errors, prefix+".timestamp must be numeric Unix milliseconds")
		}
	}

	queryIDs := make(map[string]bool)
	foundScenarios := make(map[string]bool)
	for index, value := range queries {
		prefix := fmt.Sprintf("queries[%d]", index)
		query, ok := value.(map[string]any)
		if !ok {
			errors = append(errors, prefix+" must be an object")
			continue
		}
		if extra := unknownKeys(query, map[string]bool{"id": true, "user_id": true, "scenario": true, "query": true, "relevant": true, "negative": true, "top_k": true}); len(extra) != 0 {
			errors = append(errors, fmt.Sprintf("%s has unknown fields: %v", prefix, extra))
		}
		for _, key := range []string{"id", "user_id", "query", "scenario"} {
			if text, ok := query[key].(string); !ok || text == "" {
				errors = append(errors, fmt.Sprintf("%s.%s must be a non-empty string", prefix, key))
			}
		}
		if id, ok := query["id"].(string); ok {
			if queryIDs[id] {
				errors = append(errors, fmt.Sprintf("duplicate query id %q", id))
			}
			queryIDs[id] = true
		}
		scenario, _ := query["scenario"].(string)
		if !scenarios[scenario] {
			errors = append(errors, fmt.Sprintf("%s.scenario must be one of %v", prefix, sortedSetKeys(scenarios)))
		} else {
			foundScenarios[scenario] = true
		}

		arrays := make(map[string][]string)
		for _, key := range []string{"relevant", "negative"} {
			raw, exists := query[key]
			if !exists {
				errors = append(errors, fmt.Sprintf("%s.%s is required", prefix, key))
				continue
			}
			values, valid := stringArray(raw)
			if !valid {
				errors = append(errors, fmt.Sprintf("%s.%s must be a string array", prefix, key))
				continue
			}
			arrays[key] = values
			seen := make(map[string]bool)
			duplicate := false
			for _, id := range values {
				if seen[id] {
					duplicate = true
				}
				seen[id] = true
				if !recordIDs[id] {
					errors = append(errors, fmt.Sprintf("%s.%s references unknown record %q", prefix, key, id))
				}
			}
			if duplicate {
				errors = append(errors, fmt.Sprintf("%s.%s must not contain duplicates", prefix, key))
			}
		}
		overlap := intersection(arrays["relevant"], arrays["negative"])
		if len(overlap) != 0 {
			errors = append(errors, fmt.Sprintf("%s has relevant/negative overlap: %v", prefix, overlap))
		}
		userID, _ := query["user_id"].(string)
		for _, relevant := range arrays["relevant"] {
			if recordUsers[relevant] != userID {
				errors = append(errors, fmt.Sprintf("%s marks another user's record %q relevant", prefix, relevant))
			}
		}
		if topK, exists := query["top_k"]; exists {
			value, ok := integer(topK)
			if !ok || !validTopK(int(value)) {
				errors = append(errors, prefix+".top_k must be an integer between 1 and 100")
			}
		}
	}

	missing := make([]string, 0)
	for scenario := range scenarios {
		if !foundScenarios[scenario] {
			missing = append(missing, scenario)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		errors = append(errors, fmt.Sprintf("queries missing scenarios: %v", missing))
	}
	return errors
}

func fixtureFromValue(value any) fixture {
	root := value.(map[string]any)
	version, _ := integer(root["version"])
	result := fixture{Version: int(version), Name: root["name"].(string)}
	for _, value := range root["records"].([]any) {
		record := value.(map[string]any)
		timestamp, _ := integer(record["timestamp"])
		result.Records = append(result.Records, fixtureRecord{
			ID: record["id"].(string), UserID: record["user_id"].(string), SessionID: record["session_id"].(string),
			Timestamp: timestamp, Content: record["content"].(string),
		})
	}
	for _, value := range root["queries"].([]any) {
		query := value.(map[string]any)
		resultQuery := fixtureQuery{
			ID: query["id"].(string), UserID: query["user_id"].(string), Scenario: query["scenario"].(string),
			Query: query["query"].(string), Relevant: mustStringArray(query["relevant"]), Negative: mustStringArray(query["negative"]),
		}
		if raw, exists := query["top_k"]; exists {
			value, _ := integer(raw)
			topK := int(value)
			resultQuery.TopK = &topK
		}
		result.Queries = append(result.Queries, resultQuery)
	}
	return result
}

func integer(value any) (int64, bool) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, 64)
		return parsed, err == nil
	case int:
		return int64(value), true
	case int64:
		return value, true
	default:
		return 0, false
	}
}

func stringArray(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		if stringsValue, ok := value.([]string); ok {
			return append([]string(nil), stringsValue...), true
		}
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func mustStringArray(value any) []string {
	values, _ := stringArray(value)
	return values
}

func intersection(left, right []string) []string {
	rightSet := make(map[string]bool, len(right))
	for _, value := range right {
		rightSet[value] = true
	}
	resultSet := make(map[string]bool)
	for _, value := range left {
		if rightSet[value] {
			resultSet[value] = true
		}
	}
	result := make([]string, 0, len(resultSet))
	for value := range resultSet {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedSetKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
