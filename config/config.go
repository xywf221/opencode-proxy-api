package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

	// DiagEgress enables probing each active proxy's public egress IP and
	// logging it, so the operator can verify which address family a proxy
	// actually exits through. Set via OPCODE_DIAG_EGRESS=true.
	DiagEgress bool

	// RateLimitAction is a shell command run when the number of consecutive
	// 429 "too many requests" responses reaches RateLimitActionThreshold. It
	// lets an operator rotate an external egress (e.g. a Warp tunnel) when the
	// upstream throttles the current exit. Empty disables. Set via
	// OPCODE_RATE_LIMIT_ACTION.
	RateLimitAction string

	// RateLimitActionThreshold is how many consecutive 429 responses trigger
	// RateLimitAction. Default 3 when RateLimitAction is set. Set via
	// OPCODE_RATE_LIMIT_ACTION_THRESHOLD.
	RateLimitActionThreshold int
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
	diagEgress := parseBool(os.Getenv("OPCODE_DIAG_EGRESS"))

	rateLimitAction := strings.TrimSpace(os.Getenv("OPCODE_RATE_LIMIT_ACTION"))
	rateLimitThreshold := 3
	if raw := strings.TrimSpace(os.Getenv("OPCODE_RATE_LIMIT_ACTION_THRESHOLD")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			rateLimitThreshold = n
		} else {
			slog.With("component", "config").Warn("invalid OPCODE_RATE_LIMIT_ACTION_THRESHOLD, using default",
				"value", raw, "default", rateLimitThreshold)
		}
	}

	// If single proxy is specified but no pool file, treat it as a single-entry pool
	if proxyURL != "" && proxyPoolFile == "" {
		proxyPoolFile = "<inline-single-proxy>"
	}

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
		DiagEgress:      diagEgress,
		RateLimitAction: rateLimitAction,
		RateLimitActionThreshold: rateLimitThreshold,
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

// EgressIP returns the public egress address as seen from the remote side of
// the given client (i.e. the proxy's exit IP). Used for diagnostics to confirm
// which address family a proxy actually exits through. Returns "" on any error.
func EgressIP(client *http.Client) string {
	if client == nil {
		return ""
	}
	// ifconfig.co returns the caller's public IP as plain text (IPv6 when
	// reached over IPv6, IPv4 otherwise).
	req, err := http.NewRequest(http.MethodGet, "https://ifconfig.co", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "opencode-proxy-diagnostics")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(b))
	if ip == "" {
		return ""
	}
	return ip
}

func newTransport(proxyURL string, forceIPv6 bool) (http.RoundTripper, error) {
	if proxyURL == "" {
		// Direct connections (no proxy). Clone DefaultTransport so the pool
		// limits can be raised without mutating the process-global singleton.
		t := http.DefaultTransport.(*http.Transport).Clone()
		tuneTransport(t)
		if forceIPv6 {
			// Ask the resolver for IPv6 only.
			d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
			t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, "tcp6", addr)
			}
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
		tuneTransport(t)
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
		tuneTransport(t)
		t.Proxy = nil
		t.DialContext = dialContextFromDialer(dialer)
		return t, nil

	default:
		return nil, fmt.Errorf("unsupported OPCODE_PROXY scheme %q (supported: http, https, socks5, socks5h)", u.Scheme)
	}
}

// tuneTransport raises the connection-pool upper limits on a cloned transport
// so a single upstream host can reuse more idle keep-alive connections. The
// caller passes a transport that was already cloned off DefaultTransport (and
// which is never shared), so no global state is mutated. Clients are reused
// across requests and sessions, so a larger pool pays off directly.
func tuneTransport(t *http.Transport) {
	t.MaxIdleConnsPerHost = 64
	t.MaxIdleConns = 256
	t.IdleConnTimeout = 90 * time.Second
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
