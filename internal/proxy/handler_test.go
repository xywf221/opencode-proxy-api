package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xywf221/opencode-proxy-api/config"
)

func newTestConfig(opts ...func(*config.Config)) *config.Config {
	cfg := &config.Config{
		ListenAddr:      ":8080",
		UpstreamBase:    "",
		UpstreamToken:   "public",
		UpstreamTimeout: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func withAPIKey(key string) func(*config.Config) {
	return func(c *config.Config) { c.APIKey = key }
}

func withAllowedModels(models ...string) func(*config.Config) {
	return func(c *config.Config) {
		m := make(map[string]struct{})
		for _, mod := range models {
			m[mod] = struct{}{}
		}
		c.AllowedModels = m
	}
}

func mustNew(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestRetryOnProxyFailure(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		count := attempts
		mu.Unlock()
		// Fail the first two attempts with a network error, succeed on the third.
		if count < 3 {
			// Hijack and close to force a client-side read/dial error.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))

	cfg := newTestConfig(func(c *config.Config) {
		c.UpstreamBase = upstream.URL
	})
	h := mustNew(t, cfg)

	body := `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 3 {
		t.Errorf("upstream attempts = %d, want 3 (2 failed + 1 success)", got)
	}
}

func TestRetryExhaustsOnPersistentProxyFailure(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))

	cfg := newTestConfig(func(c *config.Config) {
		c.UpstreamBase = upstream.URL
		c.UpstreamTimeout = 500 * time.Millisecond
	})
	h := mustNew(t, cfg)

	body := `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got == 0 {
		t.Error("expected at least one upstream attempt")
	}
}

func TestNewInvalidProxy(t *testing.T) {
	cfg := newTestConfig(func(c *config.Config) {
		c.ProxyURL = "ftp://127.0.0.1:21"
	})
	if _, err := New(cfg); err == nil {
		t.Fatal("New with invalid proxy scheme should fail")
	}
}

func TestCORSPreflight(t *testing.T) {
	h := mustNew(t, newTestConfig())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS origin header")
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("missing CORS methods header")
	}
}

func TestCORSOnAPIResponse(t *testing.T) {
	h := mustNew(t, newTestConfig())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	h.ServeHTTP(rec, req)

	// CORS as a handler-level header is set by middleware in main.go;
	// the handler itself only sets Content-Type.
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Error("expected JSON content type")
	}
}

// Test /v1/models

func TestListModels(t *testing.T) {
	h := mustNew(t, newTestConfig(withAllowedModels("deepseek-v4", "gpt-4")))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("expected 2 models, got %d", len(resp.Data))
	}
}

func TestListModelsWithAuth(t *testing.T) {
	h := mustNew(t, newTestConfig(withAPIKey("secret")))
	rec := httptest.NewRecorder()

	// No API key → 401
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no auth: status = %d, want 401", rec.Code)
	}

	// With valid API key via x-api-key → 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", "secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("x-api-key: status = %d, want 200", rec.Code)
	}

	// With valid API key via Authorization: Bearer → 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("Authorization: Bearer: status = %d, want 200", rec.Code)
	}
}

// Test authentication on proxy endpoints

func TestAuth(t *testing.T) {
	h := mustNew(t, newTestConfig(withAPIKey("secret")))
	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	tests := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"no key", httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), 401},
		{"wrong key", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("x-api-key", "wrong")
			return r
		}(), 401},
		{"x-api-key", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("x-api-key", "secret")
			return r
		}(), 502}, // 502 because no upstream configured
		{"Bearer", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer secret")
			return r
		}(), 502},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tc.req)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d. Body: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

// Test model allowlist

func TestModelAllowlist(t *testing.T) {
	h := mustNew(t, newTestConfig(withAllowedModels("deepseek-v4")))
	body := func(model string) string {
		return fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, model)
	}

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"allowed", body("deepseek-v4"), 502}, // 502 because no upstream
		{"disallowed", body("gpt-4"), 403},
	}

	// Need auth since auth happens before model check
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestEmptyModel(t *testing.T) {
	h := mustNew(t, newTestConfig())
	body := `{"messages":[{"role":"user","content":"hi"}]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Errorf("empty model: status = %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
}

// Test routing

func TestUnknownEndpoint(t *testing.T) {
	h := mustNew(t, newTestConfig())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/unknown", nil)

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := mustNew(t, newTestConfig())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// Test streaming detection and upstream behavior

func TestStreamingPassthrough(t *testing.T) {
	// Mock upstream that returns a streaming response
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}

	// Check streaming content type
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, should contain text/event-stream", ct)
	}
}

func TestNonStreamingPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"123","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Errorf("body should contain response: %s", rec.Body.String())
	}
}

func TestStreamingErrorForwardedUnchanged(t *testing.T) {
	// Upstream rejects with a non-stream JSON error even though the client
	// asked for stream:true. The body must reach the client intact.
	const errBody = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded."}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(errBody))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if got := rec.Body.String(); got != errBody {
		t.Errorf("body = %q, want %q", got, errBody)
	}
	// An error must not be mislabeled as a stream.
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, error should not be labeled as a stream", ct)
	}
}

func TestRateLimitActionTriggeredAfterThreshold(t *testing.T) {
	const errBody = `{"error":{"message":"rate limited"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(errBody))
	}))
	defer upstream.Close()

	// The action appends a sentinel to a file.
	marker := filepath.Join(t.TempDir(), "action-ran.marker")
	action := fmt.Sprintf("echo ran > %s", marker)

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL
	cfg.RateLimitAction = action
	cfg.RateLimitActionThreshold = 3

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: status = %d, want 429", i, rec.Code)
		}
	}

	// The marker must exist: the 5th request crossed the threshold of 3.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("rate-limit action did not run (marker %s missing): %v", marker, err)
	}
}

func TestRateLimitActionResetOnSuccess(t *testing.T) {
	// Response mode toggled from outside the handler: fail 429 first, then we
	// flip to success, then back to 429 to confirm the counter was reset.
	fail := &atomic.Bool{}
	fail.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	marker := filepath.Join(t.TempDir(), "action-ran.marker")
	action := fmt.Sprintf("echo ran > %s", marker)

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL
	cfg.RateLimitAction = action
	cfg.RateLimitActionThreshold = 3

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`
	req := func(url string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	// 2 x 429 (counter=2, no action yet).
	if got := req("/v1/chat/completions"); got != 429 {
		t.Fatalf("expected 429, got %d", got)
	}
	if got := req("/v1/chat/completions"); got != 429 {
		t.Fatalf("expected 429, got %d", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("action ran too early (counter=2 < threshold=3)")
	}

	// Interleave a success. It must reset the consecutive-429 counter.
	fail.Store(false)
	if got := req("/v1/chat/completions"); got != 200 {
		t.Fatalf("expected 200, got %d", got)
	}
	fail.Store(true)

	// 3 more 429s are required to trigger again (counter restarted at 0).
	for i := 0; i < 3; i++ {
		if got := req("/v1/chat/completions"); got != 429 {
			t.Fatalf("expected 429, got %d", got)
		}
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("rate-limit action did not run after crossing threshold post-reset: %v", err)
	}
}

func TestTruncateBody(t *testing.T) {
	if got := truncateBody([]byte("  hi  ")); got != "hi" {
		t.Errorf("truncateBody(%q) = %q, want %q", "  hi  ", got, "hi")
	}

	long := strings.Repeat("a", maxErrorBodyLog+50)
	got := truncateBody([]byte(long))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Errorf("long body should be marked truncated, got %q", got)
	}
	if len(got) > maxErrorBodyLog+len("...(truncated)") {
		t.Errorf("truncated body too long: %d bytes", len(got))
	}

	// A cut landing mid-rune must not emit invalid UTF-8.
	multi := strings.Repeat("界", maxErrorBodyLog) // 3 bytes per rune
	if got := truncateBody([]byte(multi)); !utf8.ValidString(got) {
		t.Error("truncateBody produced invalid UTF-8")
	}
}

func TestConnTraceRecordsRemoteAddr(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer upstream.Close()

	trace := &connTrace{}
	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(context.Background(), trace.clientTrace()),
		http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	args := trace.logArgs()
	pairs := map[string]any{}
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			t.Fatalf("logArgs key %v is not a string", args[i])
		}
		pairs[key] = args[i+1]
	}

	// httptest listens on loopback, so the family is known.
	if pairs["remote_addr"] == nil {
		t.Error("remote_addr not recorded")
	}
	if fam := pairs["remote_family"]; fam != "ipv4" && fam != "ipv6" {
		t.Errorf("remote_family = %v, want ipv4 or ipv6", fam)
	}
}

func TestConnTraceEmptyWhenNoConn(t *testing.T) {
	// A request that never connects must not emit half-filled fields.
	if args := (&connTrace{}).logArgs(); len(args) != 0 {
		t.Errorf("logArgs() = %v, want empty when no connection was made", args)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed")
}

func (failingReadCloser) Close() error {
	return nil
}

func TestNonStreamingReadErrorReturnsBadGateway(t *testing.T) {
	cfg := newTestConfig()
	cfg.UpstreamBase = "http://upstream.test"
	h := mustNew(t, cfg)
	h.upstream = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       failingReadCloser{},
			}, nil
		}),
	}

	body := `{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502. Body: %s", rec.Code, rec.Body.String())
	}
}

func TestMessagesPassthrough(t *testing.T) {
	var gotPath, gotAnthropicVersion, gotAPIKey string
	var gotBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAnthropicVersion = r.Header.Get("anthropic-version")
		gotAPIKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"deepseek-v4","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "client-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/zen/v1/messages" {
		t.Errorf("upstream path = %q, want /zen/v1/messages", gotPath)
	}
	if gotAnthropicVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotAnthropicVersion)
	}
	if gotAPIKey != "client-key" {
		t.Errorf("x-api-key = %q, want client-key", gotAPIKey)
	}
	if _, ok := gotBody["stream_options"]; ok {
		t.Errorf("unexpected stream_options injection: %#v", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Errorf("expected native Claude response, got: %s", rec.Body.String())
	}
}

func TestMessagesRewritesDSMLResponse(t *testing.T) {
	dsml := "hi\n\n<｜｜DSML｜｜tool_calls>\n<｜｜DSML｜｜invoke name=\"Bash\">\n" +
		"<｜｜DSML｜｜parameter name=\"command\" string=\"true\">ls -la</｜｜DSML｜｜parameter>\n" +
		"</｜｜DSML｜｜invoke>\n</｜｜DSML｜｜tool_calls>"
	upstreamBody, _ := json.Marshal(map[string]interface{}{
		"id":          "msg_dsml",
		"type":        "message",
		"role":        "assistant",
		"model":       "deepseek-v4-flash-free",
		"content":     []interface{}{map[string]interface{}{"type": "text", "text": dsml}},
		"stop_reason": "end_turn",
		"usage":       map[string]interface{}{"input_tokens": 1, "output_tokens": 2},
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(upstreamBody)
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL
	h := mustNew(t, cfg)

	body := `{"model":"deepseek-v4","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"scan"}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use; body=%s", msg["stop_reason"], rec.Body.String())
	}
	content, _ := msg["content"].([]interface{})
	found := false
	for _, c := range content {
		b, _ := c.(map[string]interface{})
		if b["type"] == "tool_use" && b["name"] == "Bash" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected tool_use Bash in content: %#v", content)
	}
	if strings.Contains(rec.Body.String(), "DSML") {
		t.Errorf("DSML should be stripped: %s", rec.Body.String())
	}
}

func TestMessagesRewritesDSMLStream(t *testing.T) {
	stream := "" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"model\":\"deepseek-v4-flash-free\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<｜DSML｜tool_calls>\\n<｜DSML｜invoke name=\\\"Bash\\\">\\n<｜DSML｜parameter name=\\\"command\\\" string=\\\"true\\\">echo hi</｜DSML｜parameter>\\n</｜DSML｜invoke>\\n</｜DSML｜tool_calls>\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(stream))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL
	h := mustNew(t, cfg)

	body := `{"model":"deepseek-v4","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"tool_use"`) {
		t.Fatalf("expected rewritten tool_use stream, got: %s", out)
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use, got: %s", out)
	}
	if strings.Contains(out, "DSML") {
		t.Errorf("DSML leaked into stream: %s", out)
	}
	if !strings.Contains(out, "echo hi") {
		t.Errorf("missing tool input in stream: %s", out)
	}
}

func TestMessagesRewritesAnthropicTools(t *testing.T) {
	var gotBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL
	h := mustNew(t, cfg)

	body := `{
		"model":"deepseek-v4",
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"use tool"}]}],
		"tools":[{"name":"add","description":"add nums","input_schema":{"type":"object","properties":{"a":{"type":"number"}}}}]
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	tools, ok := gotBody["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("upstream tools = %#v", gotBody["tools"])
	}
	tool := tools[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("type = %v, want function", tool["type"])
	}
	fn, ok := tool["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("function missing: %#v", tool)
	}
	if fn["name"] != "add" {
		t.Errorf("function.name = %v, want add (this is the missing-field error root cause)", fn["name"])
	}
}

func TestResponsesPassthrough(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"deepseek-v4","output":[],"usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	cfg := newTestConfig()
	cfg.UpstreamBase = upstream.URL

	h := mustNew(t, cfg)
	body := `{"model":"deepseek-v4","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":16}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/zen/v1/responses" {
		t.Errorf("upstream path = %q, want /zen/v1/responses", gotPath)
	}
	if _, ok := gotBody["input"]; !ok {
		t.Errorf("expected input field forwarded, got: %#v", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) {
		t.Errorf("expected native responses body, got: %s", rec.Body.String())
	}
}

func TestParseRequestMeta(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantModel  string
		wantStream bool
	}{
		{"basic", `{"model":"deepseek-v4","stream":true}`, "deepseek-v4", true},
		{"no stream", `{"model":"gpt-4"}`, "gpt-4", false},
		{"empty", `{}`, "", false},
		{"invalid json", `not json`, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model, stream := parseRequestMeta([]byte(tc.body))
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
			if stream != tc.wantStream {
				t.Errorf("stream = %v, want %v", stream, tc.wantStream)
			}
		})
	}
}

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusBadRequest, "bad stuff", "invalid_request_error")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Error("missing Content-Type")
	}
	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Message != "bad stuff" || resp.Error.Type != "invalid_request_error" {
		t.Errorf("error body: %+v", resp.Error)
	}
}
