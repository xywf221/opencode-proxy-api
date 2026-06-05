package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xywf221/opencode-proxy-api/config"
	"github.com/xywf221/opencode-proxy-api/internal/reasoning"
	"github.com/xywf221/opencode-proxy-api/internal/translate"
)

type endpointConfig struct {
	upstreamPath string
	needInject   bool
	isClaude     bool
}

var endpoints = map[string]endpointConfig{
	"/v1/chat/completions": {
		upstreamPath: "/zen/v1/chat/completions",
		needInject:   true,
	},
	"/v1/messages": {
		upstreamPath: "/zen/v1/chat/completions",
		isClaude:     true,
	},
	"/v1/responses": {
		upstreamPath: "/zen/v1/responses",
	},
}

type Handler struct {
	cfg      *config.Config
	upstream *http.Client
}

func New(cfg *config.Config) *Handler {
	return &Handler{
		cfg: cfg,
		upstream: &http.Client{
			Timeout: cfg.UpstreamTimeout,
		},
	}
}

// reqLog builds a component+request-id logger from context.
func reqLog(ctx context.Context) *slog.Logger {
	l := slog.With("component", "proxy")
	if ctx != nil {
		if id, ok := ctx.Value(contextKey("request_id")).(string); ok && id != "" {
			l = l.With("req_id", id)
		}
	}
	return l
}

type contextKey string

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := reqLog(r.Context())

	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic recovered", "panic", rec)
			writeJSONError(w, http.StatusInternalServerError, "internal server error", "server_error")
		}
	}()

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.URL.Path == "/v1/models" {
		h.handleListModels(w, r)
		return
	}

	ep, ok := endpoints[r.URL.Path]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown endpoint: "+r.URL.Path, "not_found")
		return
	}

	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}

	if !h.checkAuth(r) {
		log.Warn("authentication failed", "remote", r.RemoteAddr, "path", r.URL.Path)
		writeJSONError(w, http.StatusUnauthorized, "invalid API key", "authentication_error")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "request too large or cannot read body", "invalid_request_error")
		return
	}
	r.Body.Close()

	model, isStream := parseRequestMeta(bodyBytes)

	if model == "" {
		writeJSONError(w, http.StatusBadRequest, "model field is required", "invalid_request_error")
		return
	}
	if !h.cfg.IsModelAllowed(model) {
		log.Warn("model not allowed", "model", model)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "model not allowed: " + model})
		return
	}

	log = log.With("model", model, "stream", isStream, "endpoint", r.URL.Path)
	log.Debug("processing request")

	forwardBody := bodyBytes
	if ep.isClaude {
		forwardBody = translate.ClaudeBodyToOpenAI(bodyBytes)
	} else if ep.needInject {
		forwardBody = tryInjectReasoning(model, bodyBytes)
	}

	upstreamURL := h.cfg.UpstreamBase + ep.upstreamPath

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(forwardBody))
	if err != nil {
		log.Error("failed to create upstream request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create upstream request", "server_error")
		return
	}

	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+h.cfg.UpstreamToken)
	upstreamReq.Header.Set("x-opencode-client", "desktop")

	resp, err := h.upstream.Do(upstreamReq)
	if err != nil {
		log.Error("upstream request failed", "error", err, "upstream", upstreamURL)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err), "server_error")
		return
	}
	defer resp.Body.Close()

	log.Debug("upstream response", "status", resp.StatusCode)

	if ep.isClaude {
		if resp.StatusCode != http.StatusOK {
			out, _ := io.ReadAll(resp.Body)
			log.Warn("upstream non-200 for claude endpoint", "status", resp.StatusCode)
			writeJSON(w, resp.StatusCode, translate.OpenAIErrorToClaudeError(out))
			return
		}
		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(resp.StatusCode)
			claudeStream := translate.OpenAIStreamToClaudeStream(resp.Body)
			defer claudeStream.Close()
			io.Copy(w, claudeStream)
		} else {
			out, _ := io.ReadAll(resp.Body)
			claudeResp := translate.OpenAIResponseToClaudeResponse(out)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(claudeResp)
		}
		return
	}

	forwardHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	if isStream {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		io.Copy(w, resp.Body)
	} else {
		out, _ := io.ReadAll(resp.Body)
		w.Write(out)
	}
}

func parseRequestMeta(body []byte) (model string, stream bool) {
	var raw struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &raw) == nil {
		return raw.Model, raw.Stream
	}
	return "", false
}

func (h *Handler) checkAuth(r *http.Request) bool {
	if h.cfg.APIKey == "" {
		return true
	}
	key := r.Header.Get("x-api-key")
	if key == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			key = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if key == "" {
		return false
	}
	expected := []byte(h.cfg.APIKey)
	got := []byte(key)
	return subtle.ConstantTimeCompare(expected, got) == 1
}

var allowedHeaders = map[string]bool{
	"Content-Type":      true,
	"Cache-Control":     true,
	"Connection":        true,
	"Transfer-Encoding": true,
	"X-Request-Id":      true,
}

func forwardHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vv := range resp.Header {
		if !allowedHeaders[k] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
}

func tryInjectReasoning(model string, body []byte) []byte {
	var chatBody reasoning.ChatBody
	if json.Unmarshal(body, &chatBody) != nil {
		return body
	}
	modified := reasoning.InjectReasoningContent(model, &chatBody)
	if modified == nil || modified == &chatBody {
		return body
	}
	modifiedBytes, err := json.Marshal(modified)
	if err != nil {
		return body
	}
	return modifiedBytes
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message, errType string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"message": message,
			"type":    errType,
		},
	})
}

func (h *Handler) handleListModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	if !h.checkAuth(r) {
		writeJSONError(w, http.StatusUnauthorized, "invalid API key", "authentication_error")
		return
	}
	models := h.cfg.AllowedModels
	if len(models) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"object": "list",
			"data":   []interface{}{},
		})
		return
	}
	var data []map[string]interface{}
	for id := range models {
		data = append(data, map[string]interface{}{
			"id":       id,
			"object":   "model",
			"owned_by": "opencode",
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}
