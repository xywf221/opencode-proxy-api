package config

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xywf221/opencode-proxy-api/internal/retry"
)

// ProxyPool manages a rotating pool of proxy URLs with session affinity.
// Thread-safe for concurrent access.
type ProxyPool struct {
	mu      sync.Mutex
	proxies []string // Raw proxy URLs from file
	current int      // Index of the currently active proxy (for round-robin fallback)

	// Health tracking per proxy
	healthy       []atomic.Bool   // Health status of each proxy
	failureCounts []atomic.Uint32 // Consecutive failures per proxy
	cooldownUntil []atomic.Int64  // Unix nano timestamp when proxy can be retried

	// forceIPv6 is inherited from Config and passed to newHTTPClient.
	forceIPv6 bool
	timeout   time.Duration
}

// LoadProxyPool reads a proxy list from filePath (one per line) and returns
// a ProxyPool initialized to the first proxy. Empty lines and lines starting
// with # are ignored. Returns nil if filePath is empty or the file contains
// no valid proxies.
//
// Special case: if filePath is "<inline-single-proxy>", inlineProxy must be
// provided and is used as a single-element pool (for OPCODE_PROXY without a file).
func LoadProxyPool(filePath string, timeout time.Duration, forceIPv6 bool, inlineProxy string) (*ProxyPool, error) {
	if filePath == "" {
		return nil, nil
	}

	// Handle single inline proxy (OPCODE_PROXY without OPCODE_PROXY_POOL_FILE)
	if filePath == "<inline-single-proxy>" {
		if inlineProxy == "" {
			return nil, nil
		}
		slog.Info("using single proxy as pool",
			"proxy", RedactProxyURL(inlineProxy))
		pool := &ProxyPool{
			proxies:       []string{inlineProxy},
			current:       0,
			forceIPv6:     forceIPv6,
			timeout:       timeout,
			healthy:       make([]atomic.Bool, 1),
			failureCounts: make([]atomic.Uint32, 1),
			cooldownUntil: make([]atomic.Int64, 1),
		}
		pool.healthy[0].Store(true)
		return pool, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open proxy pool file: %w", err)
	}
	defer f.Close()

	var proxies []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Basic validation: must look like a URL
		if !strings.Contains(line, "://") {
			slog.Warn("skipping invalid proxy line",
				"file", filePath, "line", lineNum, "content", line)
			continue
		}
		proxies = append(proxies, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read proxy pool file: %w", err)
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("proxy pool file %q contains no valid proxies", filePath)
	}
	slog.Info("loaded proxy pool",
		"file", filePath, "count", len(proxies), "first", RedactProxyURL(proxies[0]))

	pool := &ProxyPool{
		proxies:       proxies,
		current:       0,
		forceIPv6:     forceIPv6,
		timeout:       timeout,
		healthy:       make([]atomic.Bool, len(proxies)),
		failureCounts: make([]atomic.Uint32, len(proxies)),
		cooldownUntil: make([]atomic.Int64, len(proxies)),
	}
	// Initialize all proxies as healthy
	for i := range pool.healthy {
		pool.healthy[i].Store(true)
	}
	return pool, nil
}

// Current returns the currently active proxy URL.
func (p *ProxyPool) Current() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proxies[p.current]
}

// Rotate advances to the next proxy in the pool (wraps around to the first
// after the last). Returns the new active proxy URL.
func (p *ProxyPool) Rotate() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = (p.current + 1) % len(p.proxies)
	slog.Info("rotated to next proxy",
		"index", p.current,
		"total", len(p.proxies),
		"proxy", RedactProxyURL(p.proxies[p.current]))
	return p.proxies[p.current]
}

// NewClient builds an HTTP client using the currently active proxy.
func (p *ProxyPool) NewClient() (*http.Client, error) {
	if p == nil {
		return newHTTPClient(p.timeout, "", p.forceIPv6)
	}
	proxyURL := p.Current()
	return newHTTPClient(p.timeout, proxyURL, p.forceIPv6)
}

// ClientForSession returns an HTTP client bound to a specific session.
// Uses consistent hashing to ensure the same session always uses the same proxy,
// unless that proxy is marked unhealthy (then falls back to next healthy proxy).
func (p *ProxyPool) ClientForSession(sessionID string) (*http.Client, error) {
	if p == nil || len(p.proxies) == 0 {
		return newHTTPClient(p.timeout, "", p.forceIPv6)
	}

	// Compute stable hash for session affinity
	h := fnv.New64a()
	h.Write([]byte(sessionID))
	preferredIndex := int(h.Sum64() % uint64(len(p.proxies)))

	now := time.Now().UnixNano()

	// Try preferred proxy first if healthy and not in cooldown
	if p.healthy[preferredIndex].Load() && p.cooldownUntil[preferredIndex].Load() <= now {
		return newHTTPClient(p.timeout, p.proxies[preferredIndex], p.forceIPv6)
	}

	// Fall back to first healthy proxy not in cooldown
	for i := 0; i < len(p.proxies); i++ {
		idx := (preferredIndex + i) % len(p.proxies)
		if p.healthy[idx].Load() && p.cooldownUntil[idx].Load() <= now {
			slog.Debug("session fallback to healthy proxy",
				"session", sessionID,
				"preferred", preferredIndex,
				"fallback", idx)
			return newHTTPClient(p.timeout, p.proxies[idx], p.forceIPv6)
		}
	}

	// All proxies unhealthy or in cooldown, try preferred anyway
	slog.Warn("all proxies unhealthy or in cooldown, using preferred",
		"session", sessionID,
		"index", preferredIndex)
	return newHTTPClient(p.timeout, p.proxies[preferredIndex], p.forceIPv6)
}

// MarkSuccess resets the failure count and cooldown for a proxy.
func (p *ProxyPool) MarkSuccess(proxyURL string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, proxy := range p.proxies {
		if proxy == proxyURL {
			p.failureCounts[i].Store(0)
			p.cooldownUntil[i].Store(0)
			wasHealthy := p.healthy[i].Swap(true)
			if !wasHealthy {
				slog.Info("proxy recovered",
					"index", i,
					"proxy", RedactProxyURL(proxyURL))
			}
			return
		}
	}
}

// MarkFailure increments failure count and marks proxy unhealthy if threshold exceeded.
// It distinguishes between proxy failures (network errors, timeouts) and upstream business errors.
// Implements exponential backoff with Retry-After header support.
func (p *ProxyPool) MarkFailure(proxyURL string, statusCode int, isNetworkError bool, retryAfterHeader string) {
	if p == nil {
		return
	}

	// Only count real proxy failures:
	// - Network errors (connection refused, timeout, DNS failure)
	// - 5xx server errors from the proxy itself
	// NOT counted as proxy failures:
	// - 4xx client errors (400, 401, 403, 404, 429) - these are upstream business logic
	// - Successful upstream responses (2xx, 3xx)

	if !isNetworkError {
		if statusCode > 0 && statusCode < 500 {
			// Business error from upstream, not a proxy failure
			return
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for i, proxy := range p.proxies {
		if proxy == proxyURL {
			failures := p.failureCounts[i].Add(1)

			// Calculate exponential backoff: 15s -> 30s -> 60s -> 120s
			baseDelay := 15 * time.Second
			maxDelay := 2 * time.Minute
			backoff := retry.ExponentialBackoff(failures, baseDelay, maxDelay)

			// Honor Retry-After if longer than our backoff
			if retryAfter := retry.ParseRetryAfter(retryAfterHeader); retryAfter > backoff {
				backoff = retryAfter
			}

			cooldownUntil := time.Now().Add(backoff)
			p.cooldownUntil[i].Store(cooldownUntil.UnixNano())

			// Mark unhealthy after 3 consecutive failures
			if failures >= 3 {
				wasHealthy := p.healthy[i].Swap(false)
				if wasHealthy {
					slog.Warn("proxy marked unhealthy",
						"index", i,
						"failures", failures,
						"cooldown", backoff,
						"proxy", RedactProxyURL(proxyURL))
				}
			} else {
				slog.Debug("proxy failure recorded",
					"index", i,
					"failures", failures,
					"cooldown", backoff,
					"proxy", RedactProxyURL(proxyURL))
			}
			return
		}
	}
}

// HealthStatus returns the health state of all proxies.
func (p *ProxyPool) HealthStatus() map[string]bool {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	status := make(map[string]bool, len(p.proxies))
	for i, proxy := range p.proxies {
		status[RedactProxyURL(proxy)] = p.healthy[i].Load()
	}
	return status
}
