package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyPool manages a rotating pool of proxy URLs.
// Thread-safe for concurrent access.
type ProxyPool struct {
	mu      sync.Mutex
	proxies []string // Raw proxy URLs from file
	current int      // Index of the currently active proxy

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
		return &ProxyPool{
			proxies:   []string{inlineProxy},
			current:   0,
			forceIPv6: forceIPv6,
			timeout:   timeout,
		}, nil
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
	return &ProxyPool{
		proxies:   proxies,
		current:   0,
		forceIPv6: forceIPv6,
		timeout:   timeout,
	}, nil
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
