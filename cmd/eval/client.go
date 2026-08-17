package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const maxResponseBytes = 16 << 20

type response struct {
	status    int
	body      []byte
	elapsedMS float64
	err       string
}

type client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newClient(cfg config) *client {
	return &client{
		baseURL: strings.TrimRight(cfg.baseURL, "/"),
		apiKey:  cfg.apiKey,
		http:    &http.Client{Timeout: cfg.timeout},
	}
}

func (c *client) request(path string, payload any) response {
	if payload == nil {
		return c.requestRaw(path, nil, true, "")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return response{err: err.Error()}
	}
	return c.requestRaw(path, body, true, "")
}

func (c *client) requestWithAuth(path string, payload any, auth bool, apiKey string) response {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{err: err.Error()}
	}
	return c.requestRaw(path, body, auth, apiKey)
}

func (c *client) requestRaw(path string, body []byte, auth bool, apiKey string) response {
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method = http.MethodPost
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+path, reader)
	if err != nil {
		return response{err: err.Error()}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		key := apiKey
		if key == "" {
			key = c.apiKey
		}
		if key != "" {
			req.Header.Set("X-Api-Key", key)
		}
	}
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return response{elapsedMS: elapsedMilliseconds(started), err: err.Error()}
	}
	limited := io.LimitReader(res.Body, maxResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	closeErr := res.Body.Close()
	elapsed := elapsedMilliseconds(started)
	if readErr != nil {
		return response{elapsedMS: elapsed, err: readErr.Error()}
	}
	if closeErr != nil {
		return response{elapsedMS: elapsed, err: closeErr.Error()}
	}
	if len(responseBody) > maxResponseBytes {
		return response{body: responseBody[:maxResponseBytes], elapsedMS: elapsed, err: "response exceeds 16 MiB"}
	}
	return response{status: res.StatusCode, body: responseBody, elapsedMS: elapsed}
}

func elapsedMilliseconds(started time.Time) float64 {
	return float64(time.Since(started)) / float64(time.Millisecond)
}

func decodeJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON: input is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid JSON: trailing value")
		}
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return value, nil
}

func decodeResponse(res response) (any, string) {
	value, err := decodeJSON(res.body)
	if err != nil {
		body := res.body
		if len(body) > 200 {
			body = body[:200]
		}
		return nil, fmt.Sprintf("%v; body=%q", err, body)
	}
	return value, ""
}

type evaluator struct {
	client *client
	out    io.Writer
	err    io.Writer
}
