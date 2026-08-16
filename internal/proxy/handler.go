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
	"net/http/httptrace"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xywf221/opencode-proxy-api/config"
	"github.com/xywf221/opencode-proxy-api/internal/reasoning"
	"github.com/xywf221/opencode-proxy-api/internal/retry"
	"github.com/xywf221/opencode-proxy-api/internal/session"
	"github.com/xywf221/opencode-proxy-api/internal/translate"
)

type endpointConfig struct {
	upstreamPath string
	needInject   bool
	// adaptClaudeTools rewrites Anthropic tools/tool_use for upstream,
	// which still validates tools as OpenAI function tools.
	adaptClaudeTools bool
	// rewriteDSML converts DeepSeek DSML tool-call text in Claude
	// responses into proper tool_use blocks (stream + non-stream).
	rewriteDSML bool
	// normalizeChatRoles rewrites OpenAI Chat Completions roles
	// (e.g. developer -> system) for upstream compatibility.
	normalizeChatRoles bool
}

var endpoints = map[string]endpointConfig{
	"/v1/chat/completions": {
		upstreamPath:       "/zen/v1/chat/completions",
		needInject:         true,
		normalizeChatRoles: true,
	},
	"/v1/messages": {
		upstreamPath:     "/zen/v1/messages",
		adaptClaudeTools: true,
		rewriteDSML:      true,
	},
	"/v1/responses": {
		upstreamPath: "/zen/v1/responses",
	},
}

type Handler struct {
	cfg      *config.Config
	upstream *http.Client

	// proxyPool is set when OPCODE_PROXY_POOL_FILE is configured.
	// When non-nil, 429 responses trigger rotation via RotateProxy().
	proxyPool *config.ProxyPool
	poolMu    sync.Mutex // Protects upstream client during rotation

	// rateLimitCount tracks consecutive 429 responses. When it reaches
	// cfg.RateLimitActionThreshold, cfg.RateLimitAction is executed and the
	// count resets. Reset to 0 on any successful response.
	rateLimitCount atomic.Int32
}

func New(cfg *config.Config) (*Handler, error) {
	var pool *config.ProxyPool
	var client *http.Client
	var err error

	if cfg.ProxyPoolFile != "" {
		pool, err = config.LoadProxyPool(cfg.ProxyPoolFile, cfg.UpstreamTimeout, cfg.ForceIPv6, cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("load proxy pool: %w", err)
		}
		if pool != nil {
			client, err = pool.NewClient()
			if err != nil {
				return nil, fmt.Errorf("create client from proxy pool: %w", err)
			}
		}
	}

	// Fallback to direct client if no pool was created
	if client == nil {
		client, err = cfg.NewUpstreamClient()
		if err != nil {
			return nil, fmt.Errorf("upstream client: %w", err)
		}
	}

	h := &Handler{
		cfg:       cfg,
		upstream:  client,
		proxyPool: pool,
	}

	// When diagnostics are enabled, probe and log the active proxy's egress
	// address once on startup.
	if cfg.DiagEgress {
		h.logActiveEgress()
	}

	return h, nil
}

// reqLog builds a component+request-id logger from context.
func reqLog(ctx context.Context) *slog.Logger {
	l := slog.With("component", "proxy")
	if id := GetRequestID(ctx); id != "" {
		l = l.With("req_id", id)
	}
	return l
}

// Package-level context key types for request ID propagation.
type ctxKey string

const reqIDKey ctxKey = "request_id"

// WithRequestID returns a context with the request ID attached.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reqIDKey, id)
}

// GetRequestID extracts the request ID from context, if any.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey).(string); ok {
		return id
	}
	return ""
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := reqLog(r.Context())

	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic recovered", "panic", rec, "stack", string(debug.Stack()))
			writeJSONError(w, http.StatusInternalServerError, "internal server error", "server_error")
		}
	}()

	// Set CORS headers on all responses
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")

	if r.Method == http.MethodOptions {
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

	// Extract session identifiers for request fingerprinting and proxy affinity
	sessionIDs := session.ExtractFromRequest(r, bodyBytes)
	log = log.With("session", sessionIDs.Session, "request_id", sessionIDs.Request)

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

	start := time.Now()

	forwardBody := bodyBytes
	if ep.normalizeChatRoles {
		forwardBody = translate.NormalizeChatCompletionRequest(forwardBody)
	}
	if ep.adaptClaudeTools {
		forwardBody = translate.ClaudeRequestToUpstream(forwardBody)
	}
	if ep.needInject {
		forwardBody = tryInjectReasoning(model, forwardBody)
	}

	baseURL := strings.TrimRight(h.cfg.UpstreamBase, "/")
	upstreamURL := baseURL + ep.upstreamPath

	trace := &connTrace{}
	upstreamCtx := httptrace.WithClientTrace(r.Context(), trace.clientTrace())

	// newReq builds a fresh upstream request. The body must be re-read each
	// attempt: bytes.NewReader consumes its buffer.
	newReq := func() (*http.Request, error) {
		upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(forwardBody))
		if err != nil {
			return nil, err
		}
		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Authorization", "Bearer "+h.cfg.UpstreamToken)
		upstreamReq.Header.Set("x-opencode-client", "desktop")
		upstreamReq.Header.Set("User-Agent", session.OpencodeUserAgent())
		upstreamReq.Header.Set("Referer", "https://opencode.ai/")
		upstreamReq.Header.Set("X-Title", "opencode")
		upstreamReq.Header.Set("x-opencode-session", sessionIDs.Session)
		upstreamReq.Header.Set("x-opencode-request", sessionIDs.Request)
		upstreamReq.Header.Set("x-opencode-project", sessionIDs.Project)
		// Forward Anthropic-compatible headers when present (used by /v1/messages clients).
		if v := r.Header.Get("anthropic-version"); v != "" {
			upstreamReq.Header.Set("anthropic-version", v)
		}
		if v := r.Header.Get("x-api-key"); v != "" {
			upstreamReq.Header.Set("x-api-key", v)
		}
		return upstreamReq, nil
	}

	// clientForSession returns the HTTP client for this session. When a proxy
	// pool is configured this respects session affinity and skips unhealthy
	// proxies; otherwise the direct upstream client is used.
	clientForSession := func() (*http.Client, error) {
		if h.proxyPool == nil {
			return h.upstream, nil
		}
		h.poolMu.Lock()
		defer h.poolMu.Unlock()
		return h.proxyPool.ClientForSession(sessionIDs.Session)
	}

	// Retry transient network/proxy failures instead of immediately returning
	// an error. A failed dial (e.g. SOCKS connection refused) is retried a few
	// times with a short backoff; each attempt selects a fresh client so a bad
	// proxy is skipped once marked unhealthy.
	const maxAttempts = 3
	var resp *http.Response
	attempt := 0
	for {
		attempt++
		upstreamReq, err := newReq()
		if err != nil {
			log.Error("failed to create upstream request", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "failed to create upstream request", "server_error")
			return
		}

		client, err := clientForSession()
		if err != nil {
			log.Error("failed to get session client", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "proxy configuration error", "server_error")
			return
		}

		resp, err = client.Do(upstreamReq)
		if err == nil {
			break
		}

		// Mark the current proxy as failed (network error).
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			h.proxyPool.MarkFailure(currentProxy, 0, true, "")
			h.poolMu.Unlock()
		}

		if attempt >= maxAttempts {
			log.Error("upstream request failed",
				append([]any{"error", err, "upstream", upstreamURL, "attempts", attempt}, trace.logArgs()...)...)
			writeJSONError(w, http.StatusBadGateway, "upstream request failed", "server_error")
			return
		}

		// Short exponential backoff between attempts (200ms, 400ms).
		backoff := retry.ExponentialBackoff(uint32(attempt), 200*time.Millisecond, 800*time.Millisecond)
		log.Warn("upstream request failed, retrying",
			"error", err, "attempt", attempt, "max_attempts", maxAttempts, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-r.Context().Done():
			return
		}
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)

	// DSML rewrite needs the full body (including streams): free DeepSeek
	// often streams tool calls as text deltas that only parse once complete.
	if isStream && !ep.rewriteDSML {
		logArgs := append([]any{"status", resp.StatusCode, "duration", elapsed.String()}, trace.logArgs()...)

		// Read the whole upstream body (whether a small JSON error or a stream)
		// so the shared postUpstream path can account health, rotate on 429 and
		// forward it. Streaming bodies are buffered before forwarding; the final
		// bytes sent to the client are identical to the upstream's.
		out, readErr := readUpstreamBody(resp.Body)
		if readErr != nil {
			log.Error("failed to read upstream error response",
				append(logArgs, "error", readErr)...)
			writeJSONError(w, http.StatusBadGateway, "upstream response error", "server_error")
			return
		}

		// The streaming early path labels a response a stream only on success;
		// an error response (>=400) must not be mislabeled, mirroring the
		// original success-only branch of this path.
		h.postUpstream(w, log, resp, out, logArgs, forwardBody, resp.StatusCode < 400)
		return
	}

	out, err := readUpstreamBody(resp.Body)
	if err != nil {
		log.Error("failed to read upstream response", "error", err)
		writeJSONError(w, http.StatusBadGateway, "upstream response error", "server_error")
		return
	}
	if ep.rewriteDSML && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if isStream {
			out = translate.RewriteClaudeStreamDSML(out)
		} else {
			out = translate.RewriteClaudeMessageDSML(out)
		}
	}
	logArgs := append([]any{"status", resp.StatusCode, "duration", elapsed.String()}, trace.logArgs()...)

	h.postUpstream(w, log, resp, out, logArgs, forwardBody, isStream)
}

// postUpstream is the shared tail for both the streaming and non-streaming
// paths once the upstream body is in memory. Keeping them identical prevents
// the two paths from drifting in observable behavior: logging, proxy-health
// accounting (MarkFailure/MarkSuccess), Retry-After handling, rate-limit
// rotation on 429, successful-response counter resets, header forwarding, and
// the final write.
//
// out is the (possibly rewritten) upstream body; for an error status it is the
// (small) JSON error payload, otherwise the response/stream body.
//
// labelStream controls whether a missing Content-Type defaults to
// text/event-stream. It is true only for responses that should look like a
// stream: the trailing path uses the request's stream flag, while the
// streaming early path labels only successful (non-error) responses.
func (h *Handler) postUpstream(w http.ResponseWriter, log *slog.Logger, resp *http.Response,
	out []byte, logArgs []any, forwardBody []byte, labelStream bool) {

	status := resp.StatusCode
	if status >= 400 {
		log.Warn("upstream error", append(logArgs, "body", truncateBody(out))...)
		logRejectedRequest(log, status, forwardBody)

		// Mark proxy failure with status code and retry-after
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			retryAfter := resp.Header.Get("Retry-After")
			h.proxyPool.MarkFailure(currentProxy, status, false, retryAfter) // HTTP error
			h.poolMu.Unlock()
		}

		// Rotate on 429 to distribute load; when the rate-limit
		// threshold is reached, run the external egress action.
		// Runs independently of proxyPool so a Warp-style egress action
		// works even without a configured pool.
		if status == http.StatusTooManyRequests {
			h.onRateLimited()
		}
	} else {
		log.Info("upstream", logArgs...)

		// A successful response means the current egress is not being throttled:
		// reset the consecutive-429 counter.
		h.rateLimitCount.Store(0)

		// Mark proxy success for health tracking
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			h.proxyPool.MarkSuccess(currentProxy)
			h.poolMu.Unlock()
		}
	}
	forwardHeaders(w, resp)
	if labelStream && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(status)
	if _, err := w.Write(out); err != nil {
		log.Debug("response write error", "error", err)
	}
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

func tryInjectReasoning(model string, body []byte) []byte {
	// Fast path: models that match no injection rule never get reasoning_content.
	// Skipping the (double) Unmarshal here avoids paying JSON cost for every
	// request to models that don't need it.
	if !reasoning.ModelRequiresInjection(model) {
		return body
	}
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

// rotateProxy advances to the next proxy in the pool and rebuilds the HTTP client.
// Called when a 429 response is received and proxyPool is configured.
func (h *Handler) rotateProxy() {
	h.poolMu.Lock()
	defer h.poolMu.Unlock()

	newProxy := h.proxyPool.Rotate()
	client, err := h.proxyPool.NewClient()
	if err != nil {
		slog.With("component", "proxy").Error("failed to rotate proxy",
			"proxy", config.RedactProxyURL(newProxy), "error", err)
		return
	}
	h.upstream = client
	h.logEgress(newProxy, client)
}

// onRateLimited is called on every 429 response. It rotates the proxy and, once
// the consecutive 429 count reaches the configured threshold, runs the external
// rate-limit action (e.g. switching a Warp egress) then resets the counter.
func (h *Handler) onRateLimited() {
	count := h.rateLimitCount.Add(1)

	// Attempt proxy rotation when a pool is present.
	if h.proxyPool != nil {
		h.rotateProxy()
	} else if h.cfg.RateLimitAction == "" {
		return // nothing actionable
	}

	action := h.cfg.RateLimitAction
	threshold := h.cfg.RateLimitActionThreshold
	if action == "" || threshold <= 0 ||
		count < int32(threshold) {
		return
	}

	// Threshold reached: run the external command, then reset the counter.
	h.rateLimitCount.Store(0)
	slog.With("component", "proxy").Warn("running rate-limit action",
		"429_count", count, "threshold", threshold, "action", action)

	cmd := rateLimitCommand(action)
	cmdOutput, err := cmd.CombinedOutput()
	if err != nil {
		slog.With("component", "proxy").Error("rate-limit action failed",
			"action", action, "error", err, "output", strings.TrimSpace(string(cmdOutput)))
		return
	}
	slog.With("component", "proxy").Info("rate-limit action completed",
		"action", action, "output", strings.TrimSpace(string(cmdOutput)))
}

// rateLimitCommand wraps action in a platform-appropriate shell. Windows has
// no /bin/sh; use cmd.exe there. On Unix, sh -c is expected.
func rateLimitCommand(action string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", action)
	}
	return exec.Command("sh", "-c", action)
}

// logEgress probes and logs the public egress IP of the given proxy/client,
// but only when OPCODE_DIAG_EGRESS is enabled. Silence on failure.
func (h *Handler) logEgress(proxyURL string, client *http.Client) {
	if client == nil || !h.cfg.DiagEgress {
		return
	}
	ip := config.EgressIP(client)
	slog.With("component", "proxy").Info("proxy egress",
		"proxy", config.RedactProxyURL(proxyURL),
		"egress", ip)
}

// logActiveEgress probes the currently active proxy's egress IP. If a proxy
// pool is configured it uses the pool's current client; otherwise the direct
// upstream client. Only logs when OPCODE_DIAG_EGRESS is enabled.
func (h *Handler) logActiveEgress() {
	if !h.cfg.DiagEgress {
		return
	}
	if h.proxyPool != nil {
		h.poolMu.Lock()
		proxyURL := h.proxyPool.Current()
		client, err := h.proxyPool.NewClient()
		h.poolMu.Unlock()
		if err != nil {
			return
		}
		h.logEgress(proxyURL, client)
		return
	}
	if h.upstream != nil {
		h.logEgress("", h.upstream)
	}
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