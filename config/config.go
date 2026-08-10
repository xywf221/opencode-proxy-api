package config

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

type Config struct {
	ListenAddr string

	APIKey string

	AllowedModels map[string]struct{}

	UpstreamBase  string
	UpstreamToken string

	// UpstreamTimeout is the timeout for upstream HTTP requests.
	// Default: 5 minutes.
	UpstreamTimeout time.Duration

	// ProxyURL is an optional upstream proxy for outbound requests.
	// Supported schemes: http, https, socks5, socks5h.
	// Example: socks5://127.0.0.1:1080, http://user:pass@127.0.0.1:8080
	ProxyURL string
}

func parseModelList(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	set := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		m := strings.TrimSpace(part)
		if m != "" {
			set[m] = struct{}{}
		}
	}
	return set
}

func Load() *Config {
	apiKey := os.Getenv("OPCODE_API_KEY")

	listen := os.Getenv("OPCODE_LISTEN")
	if listen == "" {
		listen = ":8080"
	}

	base := os.Getenv("OPCODE_UPSTREAM_BASE")
	if base == "" {
		base = "https://opencode.ai"
	}

	token := os.Getenv("OPCODE_UPSTREAM_TOKEN")
	if token == "" {
		token = "public"
	}

	timeoutStr := os.Getenv("OPCODE_UPSTREAM_TIMEOUT")
	timeout := 5 * time.Minute
	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			timeout = d
		} else {
			slog.With("component", "config").Warn("invalid OPCODE_UPSTREAM_TIMEOUT, using default", "value", timeoutStr, "default", timeout)
		}
	}

	return &Config{
		ListenAddr:      listen,
		APIKey:          apiKey,
		AllowedModels:   parseModelList(os.Getenv("OPCODE_ALLOWED_MODELS")),
		UpstreamBase:    base,
		UpstreamToken:   token,
		UpstreamTimeout: timeout,
		ProxyURL:        strings.TrimSpace(os.Getenv("OPCODE_PROXY")),
	}
}

func (c *Config) IsModelAllowed(model string) bool {
	if len(c.AllowedModels) == 0 {
		return true
	}
	_, ok := c.AllowedModels[model]
	return ok
}

// NewUpstreamClient builds an HTTP client for upstream requests.
// When ProxyURL is set, supports http, https, socks5, and socks5h proxies.
// socks5 resolves DNS locally; socks5h resolves DNS on the proxy host.
func (c *Config) NewUpstreamClient() (*http.Client, error) {
	return newHTTPClient(c.UpstreamTimeout, c.ProxyURL)
}

func newHTTPClient(timeout time.Duration, proxyURL string) (*http.Client, error) {
	transport, err := newTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

func newTransport(proxyURL string) (http.RoundTripper, error) {
	if proxyURL == "" {
		return http.DefaultTransport, nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid OPCODE_PROXY URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid OPCODE_PROXY URL: missing host")
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = http.ProxyURL(u)
		return t, nil

	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{
				User:     u.User.Username(),
				Password: password,
			}
		}
		// SOCKS5 dialer sends hostnames to the proxy (remote DNS = socks5h).
		base, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 proxy setup failed: %w", err)
		}
		dialer := proxy.Dialer(base)
		if scheme == "socks5" {
			// Local DNS resolution, then dial resolved IP through the proxy.
			dialer = &localResolveDialer{Dialer: base}
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = dialContextFromDialer(dialer)
		return t, nil

	default:
		return nil, fmt.Errorf("unsupported OPCODE_PROXY scheme %q (supported: http, https, socks5, socks5h)", u.Scheme)
	}
}

// localResolveDialer resolves the hostname locally before dialing through the proxy.
type localResolveDialer struct {
	proxy.Dialer
}

func (d *localResolveDialer) Dial(network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs found for host %q", host)
	}
	return d.Dialer.Dial(network, net.JoinHostPort(ips[0].String(), port))
}

func dialContextFromDialer(d proxy.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Best-effort cancellation: Dial ignores ctx; check before/after.
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			c, err := d.Dial(network, addr)
			ch <- result{c, err}
		}()
		select {
		case <-ctx.Done():
			go func() {
				r := <-ch
				if r.conn != nil {
					r.conn.Close()
				}
			}()
			return nil, ctx.Err()
		case r := <-ch:
			return r.conn, r.err
		}
	}
}
