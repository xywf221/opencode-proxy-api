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

	// ProxyPoolFile is a file containing one proxy URL per line.
	// When set, the proxy rotates on 429 responses.
	ProxyPoolFile string

	// ForceIPv6 forces IPv6-only resolution when ProxyURL uses socks5 (local resolve).
	// Has no effect on socks5h (remote resolve) or http/https proxies.
	// Set via OPCODE_FORCE_IPV6=true.
	ForceIPv6 bool
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

	proxyURL := strings.TrimSpace(os.Getenv("OPCODE_PROXY"))
	proxyPoolFile := strings.TrimSpace(os.Getenv("OPCODE_PROXY_POOL_FILE"))
	forceIPv6 := parseBool(os.Getenv("OPCODE_FORCE_IPV6"))

	if forceIPv6 {
		// The flag only bites where this process resolves DNS itself. Warn
		// loudly rather than let it look effective when it cannot be.
		log := slog.With("component", "config")
		switch proxyScheme(proxyURL) {
		case "socks5h":
			log.Warn("OPCODE_FORCE_IPV6 has no effect with socks5h: the proxy resolves DNS and picks the address family. Use socks5:// to force IPv6 locally, or set the proxy's own IPv6 preference")
		case "http", "https":
			log.Warn("OPCODE_FORCE_IPV6 has no effect with an http/https proxy: the proxy resolves DNS and picks the address family")
		}
	}

	return &Config{
		ListenAddr:      listen,
		APIKey:          apiKey,
		AllowedModels:   parseModelList(os.Getenv("OPCODE_ALLOWED_MODELS")),
		UpstreamBase:    base,
		UpstreamToken:   token,
		UpstreamTimeout: timeout,
		ProxyURL:        proxyURL,
		ProxyPoolFile:   proxyPoolFile,
		ForceIPv6:       forceIPv6,
	}
}

// parseBool accepts the usual truthy spellings for env-var flags.
func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// proxyScheme returns the lowercased scheme of proxyURL, or "" if absent.
func proxyScheme(proxyURL string) string {
	if proxyURL == "" {
		return ""
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

func (c *Config) IsModelAllowed(model string) bool {
	if len(c.AllowedModels) == 0 {
		return true
	}
	_, ok := c.AllowedModels[model]
	return ok
}

// RedactProxyURL returns proxyURL with any userinfo credentials masked, for
// safe logging. Unparseable input is reported as "<invalid proxy url>" rather
// than echoed, since it may still contain a password.
func RedactProxyURL(proxyURL string) string {
	if proxyURL == "" {
		return ""
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return "<invalid proxy url>"
	}
	if u.User == nil {
		return u.String()
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return u.String()
	}
	// Build the string manually: url.String() percent-encodes userinfo, which
	// would render the mask as %2A%2A%2A.
	rest := u.User.Username() + ":***@" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if u.Scheme == "" {
		return rest
	}
	return u.Scheme + "://" + rest
}

// NewUpstreamClient builds an HTTP client for upstream requests.
// When ProxyURL is set, supports http, https, socks5, and socks5h proxies.
// socks5 resolves DNS locally; socks5h resolves DNS on the proxy host.
func (c *Config) NewUpstreamClient() (*http.Client, error) {
	return newHTTPClient(c.UpstreamTimeout, c.ProxyURL, c.ForceIPv6)
}

func newHTTPClient(timeout time.Duration, proxyURL string, forceIPv6 bool) (*http.Client, error) {
	transport, err := newTransport(proxyURL, forceIPv6)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

func newTransport(proxyURL string, forceIPv6 bool) (http.RoundTripper, error) {
	if proxyURL == "" {
		if !forceIPv6 {
			return http.DefaultTransport, nil
		}
		// Direct connections: ask the resolver for IPv6 only.
		t := http.DefaultTransport.(*http.Transport).Clone()
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp6", addr)
		}
		return t, nil
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
			dialer = &localResolveDialer{Dialer: base, forceIPv6: forceIPv6}
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = dialContextFromDialer(dialer)
		return t, nil

	default:
		return nil, fmt.Errorf("unsupported OPCODE_PROXY scheme %q (supported: http, https, socks5, socks5h)", u.Scheme)
	}
}

// localResolveDialer resolves the hostname locally before dialing through the
// proxy. Every resolved address is tried in order, so one dead address family
// does not fail the request.
type localResolveDialer struct {
	proxy.Dialer

	// forceIPv6 drops IPv4 candidates, failing rather than falling back.
	forceIPv6 bool
}

func (d *localResolveDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *localResolveDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	// An address that is already a literal IP needs no lookup.
	if ip := net.ParseIP(host); ip != nil {
		if d.forceIPv6 && ip.To4() != nil {
			return nil, fmt.Errorf("OPCODE_FORCE_IPV6 is set but %q is an IPv4 literal", host)
		}
		return d.dialOne(ctx, network, address)
	}

	resolver := net.DefaultResolver
	lookupNet := "ip"
	if d.forceIPv6 {
		lookupNet = "ip6"
	}
	addrs, err := resolver.LookupIP(ctx, lookupNet, host)
	if err != nil {
		if d.forceIPv6 {
			return nil, fmt.Errorf("no AAAA record for host %q (OPCODE_FORCE_IPV6 is set): %w", host, err)
		}
		return nil, err
	}
	if len(addrs) == 0 {
		if d.forceIPv6 {
			return nil, fmt.Errorf("no AAAA record for host %q (OPCODE_FORCE_IPV6 is set)", host)
		}
		return nil, fmt.Errorf("no IPs found for host %q", host)
	}

	// Log the resolved addresses so users can verify forceIPv6 is working.
	slog.With("component", "proxy").Info("resolved host",
		"host", host,
		"addrs", addrs,
		"force_ipv6", d.forceIPv6,
	)

	var firstErr error
	for _, ip := range addrs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := d.dialOne(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("all %d addresses for %q failed: %w", len(addrs), host, firstErr)
}

// dialOne dials a single resolved address, honoring ctx if the underlying
// dialer supports it.
func (d *localResolveDialer) dialOne(ctx context.Context, network, address string) (net.Conn, error) {
	if cd, ok := d.Dialer.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, address)
	}
	return d.Dialer.Dial(network, address)
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
