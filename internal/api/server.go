// Package api exposes the leaderboard's Add/Search HTTP contract.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/latebit-io/memoryleaderboard/internal/memory"
)

// maxTopK bounds Search fan-out; the benchmark contract never exceeds 100.
const maxTopK = 100

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
	Query   string   `json:"query"`
	Options []string `json:"options,omitempty"`
	UserID  string   `json:"user_id"`
	TopK    *int     `json:"top_k"`
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

// Config carries the runtime's decisions to the HTTP layer.
type Config struct {
	// APIKey enforces auth on /add and /search. Empty installs no auth
	// middleware; requiring a key (or an explicit open-mode opt-out) is
	// the runtime wiring's decision, not this layer's.
	APIKey string
	// NavEnabled reports whether agentic search is active, surfaced on
	// /healthz so operators and tests can see the mode without reading
	// logs.
	NavEnabled bool
	// DistillEnabled reports whether Add-time LLM distillation is active.
	DistillEnabled bool
}

// Routes registers /add and /search plus a health probe.
func Routes(e *echo.Echo, h *Handler, cfg Config) {
	g := e.Group("")
	if cfg.APIKey != "" {
		g.Use(authMiddleware(cfg.APIKey))
	}
	g.POST("/add", h.Add)
	g.POST("/search", h.Search)
	health := func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status": "ok", "nav": cfg.NavEnabled, "distill": cfg.DistillEnabled,
		})
	}
	e.GET("/health", health)
	e.GET("/healthz", health)
}

// jsonErr is the single error-body shape for all endpoints.
func jsonErr(c *echo.Context, code int, msg string) error {
	return c.JSON(code, map[string]string{"error": msg})
}

// authMiddleware accepts Token/Bearer Authorization or X-Api-Key, per the
// leaderboard's supported schemes. It always enforces its key.
func authMiddleware(apiKey string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
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
				return jsonErr(c, http.StatusUnauthorized, "unauthorized")
			}
			return next(c)
		}
	}
}

// Add persists synchronously; data must be searchable on return.
func (h *Handler) Add(c *echo.Context) error {
	var req AddRequest
	if err := c.Bind(&req); err != nil {
		return jsonErr(c, http.StatusBadRequest, "invalid json")
	}
	if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.RequestID) == "" ||
		strings.TrimSpace(req.SessionID) == "" || len(req.Messages) == 0 {
		return jsonErr(c, http.StatusBadRequest, "request_id, user_id, session_id, messages required")
	}
	for _, message := range req.Messages {
		if strings.TrimSpace(message.Role) == "" || strings.TrimSpace(message.Content) == "" {
			return jsonErr(c, http.StatusBadRequest, "message role and content required")
		}
	}
	if err := h.store.Add(c.Request().Context(), req.UserID, req.SessionID, req.RequestID, req.Messages); err != nil {
		if errors.Is(err, memory.ErrAddConflict) {
			return jsonErr(c, http.StatusConflict, err.Error())
		}
		// 503 is in the harness retry set; surface transient store trouble as retryable.
		return jsonErr(c, http.StatusServiceUnavailable, err.Error())
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
		return jsonErr(c, http.StatusBadRequest, "invalid json")
	}
	if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.Query) == "" {
		return jsonErr(c, http.StatusBadRequest, "query, user_id required")
	}
	if req.TopK == nil || *req.TopK <= 0 || *req.TopK > maxTopK {
		return jsonErr(c, http.StatusBadRequest, "top_k must be between 1 and 100")
	}
	for _, option := range req.Options {
		if strings.TrimSpace(option) == "" {
			return jsonErr(c, http.StatusBadRequest, "options must contain non-empty strings")
		}
	}
	topK := *req.TopK
	query := req.Query
	if len(req.Options) > 0 {
		query += "\n\nAnswer choices:\n- " + strings.Join(req.Options, "\n- ")
	}
	records, err := h.store.Search(c.Request().Context(), req.UserID, query, topK)
	if err != nil {
		return jsonErr(c, http.StatusServiceUnavailable, err.Error())
	}
	if records == nil {
		records = []memory.Record{}
	} else if len(records) > topK {
		records = records[:topK]
	}
	return c.JSON(http.StatusOK, SearchResponse{Data: records})
}
