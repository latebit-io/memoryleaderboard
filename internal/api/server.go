// Package api exposes the leaderboard's Add/Search HTTP contract.
package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

// AddRequest is the harness Add payload.
type AddRequest struct {
	RequestID string           `json:"request_id"`
	Messages  []memory.Message `json:"messages"`
	UserID    string           `json:"user_id"`
	SessionID string           `json:"session_id"`
}

// AddResponse echoes the ids after persistence.
type AddResponse struct {
	Success   bool   `json:"success"`
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
}

// SearchRequest is the harness Search payload.
type SearchRequest struct {
	Query   string         `json:"query"`
	Options map[string]any `json:"options,omitempty"`
	UserID  string         `json:"user_id"`
	TopK    int            `json:"top_k"`
}

// SearchResponse carries ranked memory evidence, most relevant first.
type SearchResponse struct {
	Data []memory.Record `json:"data"`
}

// Handler serves the two contract endpoints against a Store.
type Handler struct {
	store memory.Store
}

// NewHandler wires a store.
func NewHandler(store memory.Store) *Handler {
	return &Handler{store: store}
}

// Routes registers /add and /search plus a health probe.
func Routes(e *echo.Echo, h *Handler, apiKey string) {
	g := e.Group("", authMiddleware(apiKey))
	g.POST("/add", h.Add)
	g.POST("/search", h.Search)
	e.GET("/healthz", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
}

// authMiddleware accepts Token/Bearer Authorization or X-Api-Key, per the
// leaderboard's supported schemes. Empty key disables auth (local dev).
func authMiddleware(apiKey string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if apiKey == "" {
				return next(c)
			}
			got := c.Request().Header.Get("X-Api-Key")
			if got == "" {
				auth := c.Request().Header.Get("Authorization")
				for _, scheme := range []string{"Bearer ", "Token "} {
					if strings.HasPrefix(auth, scheme) {
						got = strings.TrimPrefix(auth, scheme)
						break
					}
				}
			}
			if got != apiKey {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
			return next(c)
		}
	}
}

// Add persists synchronously; data must be searchable on return.
func (h *Handler) Add(c *echo.Context) error {
	var req AddRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json"})
	}
	if req.UserID == "" || req.RequestID == "" || len(req.Messages) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "request_id, user_id, messages required"})
	}
	if err := h.store.Add(c.Request().Context(), req.UserID, req.SessionID, req.RequestID, req.Messages); err != nil {
		// 503 is in the harness retry set; surface transient store trouble as retryable.
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, AddResponse{
		Success:   true,
		RequestID: req.RequestID,
		UserID:    req.UserID,
		SessionID: req.SessionID,
	})
}

// Search returns ranked evidence only; answer generation is the platform's.
func (h *Handler) Search(c *echo.Context) error {
	var req SearchRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json"})
	}
	if req.UserID == "" || req.Query == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "query, user_id required"})
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 100
	}
	records, err := h.store.Search(c.Request().Context(), req.UserID, req.Query, topK)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	if records == nil {
		records = []memory.Record{}
	}
	return c.JSON(http.StatusOK, SearchResponse{Data: records})
}
