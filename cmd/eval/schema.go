package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

type message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

type addRequest struct {
	RequestID string    `json:"request_id"`
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	Messages  []message `json:"messages"`
}

type searchRequest struct {
	Query   string   `json:"query"`
	Options []string `json:"options,omitempty"`
	UserID  string   `json:"user_id"`
	TopK    int      `json:"top_k"`
}

func addPayload(requestID, userID, sessionID, content string, timestamp int64) addRequest {
	return addRequest{
		RequestID: requestID,
		UserID:    userID,
		SessionID: sessionID,
		Messages:  []message{{Role: "user", Content: content, Timestamp: timestamp}},
	}
}

func resultErrors(body any, topK int) []string {
	object, ok := body.(map[string]any)
	if !ok {
		return []string{"response must be an object with data array"}
	}
	data, ok := object["data"].([]any)
	if !ok {
		return []string{"response must be an object with data array"}
	}
	errors := make([]string, 0)
	if len(object) != 1 {
		errors = append(errors, fmt.Sprintf("response fields must be exactly [data], got %v", sortedKeys(object)))
	}
	if len(data) > topK {
		errors = append(errors, fmt.Sprintf("returned %d records for top_k=%d", len(data), topK))
	}
	scores := make([]float64, 0, len(data))
	ids := make([]string, 0, len(data))
	for index, item := range data {
		record, ok := item.(map[string]any)
		if !ok {
			errors = append(errors, fmt.Sprintf("data[%d] is not an object", index))
			continue
		}
		extra := unknownKeys(record, map[string]bool{"id": true, "content": true, "score": true, "created_at": true})
		if len(extra) != 0 {
			errors = append(errors, fmt.Sprintf("data[%d] has unknown fields: %v", index, extra))
		}
		id, ok := record["id"].(string)
		if !ok || id == "" {
			errors = append(errors, fmt.Sprintf("data[%d].id must be a non-empty string", index))
		} else {
			ids = append(ids, id)
		}
		if _, ok := record["content"].(string); !ok {
			errors = append(errors, fmt.Sprintf("data[%d].content must be a string", index))
		}
		score, ok := numeric(record["score"])
		if !ok || math.IsInf(score, 0) || math.IsNaN(score) {
			errors = append(errors, fmt.Sprintf("data[%d].score must be finite numeric", index))
		} else {
			scores = append(scores, score)
		}
		if createdAt, exists := record["created_at"]; exists {
			if _, ok := createdAt.(string); !ok {
				errors = append(errors, fmt.Sprintf("data[%d].created_at must be a string when present", index))
			}
		}
	}
	if len(scores) == len(data) {
		for index := 1; index < len(scores); index++ {
			if scores[index-1] < scores[index] {
				errors = append(errors, "scores are not descending (non-increasing)")
				break
			}
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			errors = append(errors, "result IDs must be unique")
			break
		}
		seen[id] = true
	}
	return errors
}

func dataItems(body any) []map[string]any {
	object, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	data, ok := object["data"].([]any)
	if !ok {
		return nil
	}
	items := make([]map[string]any, 0, len(data))
	for _, value := range data {
		if item, ok := value.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items
}

func contentItems(body any) []map[string]any {
	items := make([]map[string]any, 0)
	for _, item := range dataItems(body) {
		if _, ok := item["content"].(string); ok {
			items = append(items, item)
		}
	}
	return items
}

func numeric(value any) (float64, bool) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func unknownKeys(object map[string]any, allowed map[string]bool) []string {
	keys := make([]string, 0)
	for key := range object {
		if !allowed[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
