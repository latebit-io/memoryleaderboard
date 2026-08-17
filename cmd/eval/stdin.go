package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func runPrefixes(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := readResponseInput(stdin)
	if err != nil {
		writef(stderr, "ERROR prefixes: %v\n", err)
		return 1
	}
	value, err := decodeJSON(body)
	if err != nil {
		writef(stderr, "ERROR prefixes: %v\n", err)
		return 1
	}
	if errors := resultErrors(value, 100); len(errors) != 0 {
		writef(stderr, "ERROR prefixes: %s\n", strings.Join(errors, "; "))
		return 1
	}
	prefixes := make(map[string]bool)
	for _, item := range dataItems(value) {
		id, ok := item["id"].(string)
		if !ok {
			continue
		}
		parts := strings.Split(id, "/")
		if len(parts) < 3 || parts[0] != "" || parts[1] != "u" || parts[2] == "" {
			writef(stderr, "ERROR prefixes: invalid scoped record id %q\n", id)
			return 1
		}
		prefixes["/u/"+parts[2]] = true
	}
	ordered := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		ordered = append(ordered, prefix)
	}
	sort.Strings(ordered)
	writeLine(stdout, strings.Join(ordered, " "))
	return 0
}

func runNavCheck(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := readResponseInput(stdin)
	if err != nil {
		writef(stderr, "ERROR nav-check: %v\n", err)
		return 1
	}
	value, err := decodeJSON(body)
	if err != nil {
		writef(stderr, "ERROR nav-check: %v\n", err)
		return 1
	}
	data, ok := responseData(value)
	if !ok {
		writeLine(stderr, "ERROR nav-check: response must be an object with data array")
		return 1
	}
	writef(stdout, "%d records\n", len(data))
	relevant := 0
	noise := 0
	for index, raw := range data {
		record, ok := raw.(map[string]any)
		if !ok {
			writef(stderr, "ERROR nav-check: data[%d] is not an object\n", index)
			return 1
		}
		id, idOK := record["id"].(string)
		content, contentOK := record["content"].(string)
		if !idOK || !contentOK {
			writef(stderr, "ERROR nav-check: data[%d] requires string id and content\n", index)
			return 1
		}
		first := content
		if newline := strings.IndexByte(first, '\n'); newline >= 0 {
			first = first[:newline]
		}
		writef(stdout, "  %d. %s score=%v %s\n", index+1, id, record["score"], first)
		if strings.Contains(content, "CI") || strings.Contains(content, "flaky") {
			relevant++
		}
		if strings.Contains(content, "caddy") {
			noise++
		}
	}
	writef(stdout, "relevant=%d/2 noise=%d\n", relevant, noise)
	if relevant >= 1 && noise == 0 {
		return 0
	}
	return 1
}

func readResponseInput(stdin io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	marker := strings.LastIndex(text, "HTTPSTATUS:")
	if marker < 0 {
		return raw, nil
	}
	status, err := strconv.Atoi(strings.TrimSpace(text[marker+len("HTTPSTATUS:"):]))
	if err != nil {
		return nil, fmt.Errorf("invalid HTTPSTATUS marker")
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP status %d", status)
	}
	return raw[:marker], nil
}
