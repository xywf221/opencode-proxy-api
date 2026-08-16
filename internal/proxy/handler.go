package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

		// Error responses are small JSON, not a stream: buffer them so the
		// upstream error type reaches the log, then forward unchanged.
		if resp.StatusCode >= 400 {
			errBody, readErr := readUpstreamBody(resp.Body)
			if readErr != nil {
				log.Error("failed to read upstream error response",
					append(logArgs, "error", readErr)...)
				writeJSONError(w, http.StatusBadGateway, "upstream response error", "server_error")
				return
			}
			log.Warn("upstream error", append(logArgs, "body", truncateBody(errBody))...)
			logRejectedRequest(log, resp.StatusCode, forwardBody)

			// Mark proxy failure with status code and retry-after
			if h.proxyPool != nil {
				h.poolMu.Lock()
				currentProxy := h.proxyPool.Current()
				retryAfter := resp.Header.Get("Retry-After")
				h.proxyPool.MarkFailure(currentProxy, resp.StatusCode, false, retryAfter) // HTTP error
				h.poolMu.Unlock()

				// Rotate on 429 to distribute load
				if resp.StatusCode == http.StatusTooManyRequests {
					h.rotateProxy()
				}
			}

			forwardHeaders(w, resp)
			w.WriteHeader(resp.StatusCode)
			if _, err := w.Write(errBody); err != nil {
				log.Debug("response write error", "error", err)
			}
			return
		}

		log.Info("upstream", logArgs...)

		// Mark proxy success for health tracking
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			h.proxyPool.MarkSuccess(currentProxy)
			h.poolMu.Unlock()
		}

		forwardHeaders(w, resp)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			log.Debug("response write error", "error", err)
		}
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
	if resp.StatusCode >= 400 {
		log.Warn("upstream error", append(logArgs, "body", truncateBody(out))...)
		logRejectedRequest(log, resp.StatusCode, forwardBody)

		// Mark proxy failure with status code and retry-after
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			retryAfter := resp.Header.Get("Retry-After")
			h.proxyPool.MarkFailure(currentProxy, resp.StatusCode, false, retryAfter) // HTTP error
			h.poolMu.Unlock()

			// Rotate on 429 to distribute load
			if resp.StatusCode == http.StatusTooManyRequests {
				h.rotateProxy()
			}
		}
	} else {
		log.Info("upstream", logArgs...)

		// Mark proxy success for health tracking
		if h.proxyPool != nil {
			h.poolMu.Lock()
			currentProxy := h.proxyPool.Current()
			h.proxyPool.MarkSuccess(currentProxy)
			h.poolMu.Unlock()
		}
	}
	forwardHeaders(w, resp)
	if isStream && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(out); err != nil {
		log.Debug("response write error", "error", err)
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

// rotateProxy advances to the next proxy in the pool and rebuilds the HTTP client.
// Called when a 429 response is received and proxyPool is configured.
func (h *Handler) rotateProxy() {
	h.poolMu.Lock()
	newProxy := h.proxyPool.Rotate()
	client, err := h.proxyPool.NewClient()
	if err != nil {
		h.poolMu.Unlock()
		slog.With("component", "proxy").Error("failed to rotate proxy",
			"proxy", config.RedactProxyURL(newProxy), "error", err)
		return
	}
	h.upstream = client
	h.poolMu.Unlock()

	// Probe the new proxy's egress address when diagnostics are enabled.
	h.logEgress(newProxy, client)
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

// connTrace captures the remote address of the TCP connection used for an
// upstream request. When OPCODE_PROXY is set this is the proxy's address, not
// the upstream host: the proxy's own outbound hop is not observable from here.
type connTrace struct {
	mu sync.Mutex

	// remoteAddr is the peer we connected to (upstream host, or the proxy).
	remoteAddr string
	// network is "tcp4" or "tcp6" as reported by the connection.
	network string
	// reused reports whether the connection came from the idle pool, in which
	// case no fresh DNS or dial happened for this request.
	reused bool
	// dnsHost is the name looked up, and dnsAddrs the resolved candidates.
	// Empty when the transport dialed without a DNS step (proxy, or cache hit).
	dnsHost  string
	dnsAddrs []string
}

// clientTrace returns a httptrace.ClientTrace that records connection details
// into t. The returned trace is safe for the concurrent callbacks httptrace makes.
func (t *connTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.dnsHost = info.Host
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.dnsAddrs = make([]string, 0, len(info.Addrs))
			for _, a := range info.Addrs {
				t.dnsAddrs = append(t.dnsAddrs, a.IP.String())
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.reused = info.Reused
			if info.Conn == nil {
				return
			}
			if ra := info.Conn.RemoteAddr(); ra != nil {
				t.remoteAddr = ra.String()
				t.network = ra.Network()
			}
		},
	}
}

// logArgs returns slog key/value pairs describing the connection. Keys are
// omitted when the corresponding step did not happen.
func (t *connTrace) logArgs() []any {
	t.mu.Lock()
	defer t.mu.Unlock()

	args := make([]any, 0, 10)
	if t.remoteAddr == "" {
		return args
	}
	args = append(args, "remote_addr", t.remoteAddr)

	// Prefer the address family of the actual peer IP over conn.Network(),
	// which reports "tcp" for dual-stack listeners on most platforms.
	if host, _, err := net.SplitHostPort(t.remoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() != nil {
				args = append(args, "remote_family", "ipv4")
			} else {
				args = append(args, "remote_family", "ipv6")
			}
		}
	} else if t.network != "" {
		args = append(args, "remote_network", t.network)
	}

	if t.reused {
		// No DNS or dial happened; remote_addr comes from the pooled conn.
		args = append(args, "conn_reused", true)
	}
	if t.dnsHost != "" {
		args = append(args, "dns_host", t.dnsHost)
	}
	if len(t.dnsAddrs) > 0 {
		args = append(args, "dns_addrs", strings.Join(t.dnsAddrs, ","))
	}
	return args
}

// maxErrorBodyLog caps how much of a failed upstream body reaches the log.
const maxErrorBodyLog = 512

// truncateBody trims b to a rune-safe prefix suitable for logging.
func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= maxErrorBodyLog {
		return s
	}
	cut := s[:maxErrorBodyLog]
	// Avoid emitting a partial multi-byte rune at the cut point.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "...(truncated)"
}

// logRejectedRequest dumps the request body that upstream rejected with a 4xx
// status, so schema/validation failures are diagnosable from the log alone.
// It also reports whether the body is valid JSON.
func logRejectedRequest(log *slog.Logger, status int, body []byte) {
	if status < 400 || status >= 500 {
		return // only validation-type rejections
	}
	var validJSON bool
	if json.Valid(body) {
		validJSON = true
	}
	log.Warn("request rejected by upstream",
		"status", status,
		"valid_json", validJSON,
		"request_body", truncateBody(body),
	)
}

const maxUpstreamBodySize = 50 << 20 // 50 MB

// readUpstreamBody reads the upstream response body, limiting to maxUpstreamBodySize.
func readUpstreamBody(body io.ReadCloser) ([]byte, error) {
	limited := io.LimitReader(body, maxUpstreamBodySize+1)
	out, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxUpstreamBodySize {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", maxUpstreamBodySize)
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.With("component", "proxy").Error("write json error", "error", err)
	}
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
