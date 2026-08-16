package proxy

import (
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
)

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