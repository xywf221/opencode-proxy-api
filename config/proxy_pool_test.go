package config

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadProxyPool(t *testing.T) {
	// Create a temp file with test proxies
	tmpDir := t.TempDir()
	proxyFile := filepath.Join(tmpDir, "proxies.txt")
	content := `# Comment line
http://proxy1:8080

socks5://user:pass@proxy2:1080
# Another comment
http://proxy3:3128
`
	if err := os.WriteFile(proxyFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false, "")
	if err != nil {
		t.Fatalf("LoadProxyPool failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	// Should have loaded 3 proxies (skipping comments and empty lines)
	if len(pool.proxies) != 3 {
		t.Errorf("expected 3 proxies, got %d", len(pool.proxies))
	}

	// Check first proxy
	if got := pool.Current(); got != "http://proxy1:8080" {
		t.Errorf("expected first proxy http://proxy1:8080, got %s", got)
	}
}

func TestProxyPoolRotate(t *testing.T) {
	tmpDir := t.TempDir()
	proxyFile := filepath.Join(tmpDir, "proxies.txt")
	content := "http://p1:8080\nhttp://p2:8080\nhttp://p3:8080\n"
	if err := os.WriteFile(proxyFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false, "")
	if err != nil {
		t.Fatal(err)
	}

	// Initial state: p1
	if got := pool.Current(); got != "http://p1:8080" {
		t.Errorf("initial: expected p1, got %s", got)
	}

	// Rotate to p2
	if got := pool.Rotate(); got != "http://p2:8080" {
		t.Errorf("rotate 1: expected p2, got %s", got)
	}

	// Rotate to p3
	if got := pool.Rotate(); got != "http://p3:8080" {
		t.Errorf("rotate 2: expected p3, got %s", got)
	}

	// Rotate wraps back to p1
	if got := pool.Rotate(); got != "http://p1:8080" {
		t.Errorf("rotate 3: expected p1 (wrap), got %s", got)
	}
}

func TestLoadProxyPoolEmpty(t *testing.T) {
	// Empty file path returns nil
	pool, err := LoadProxyPool("", 5*time.Minute, false, "")
	if err != nil {
		t.Errorf("expected no error for empty path, got %v", err)
	}
	if pool != nil {
		t.Error("expected nil pool for empty path")
	}
}

func TestLoadProxyPoolNoValidProxies(t *testing.T) {
	tmpDir := t.TempDir()
	proxyFile := filepath.Join(tmpDir, "proxies.txt")
	content := "# Only comments\n\n# No valid proxies\n"
	if err := os.WriteFile(proxyFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false, "")
	if err == nil {
		t.Error("expected error for file with no valid proxies")
	}
	if pool != nil {
		t.Error("expected nil pool when no valid proxies")
	}
}

func TestLoadProxyPoolInvalidLines(t *testing.T) {
	tmpDir := t.TempDir()
	proxyFile := filepath.Join(tmpDir, "proxies.txt")
	content := `http://valid:8080
not-a-url
another-invalid-line
socks5://valid2:1080
`
	if err := os.WriteFile(proxyFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false, "")
	if err != nil {
		t.Fatalf("LoadProxyPool failed: %v", err)
	}

	// Should skip invalid lines and load only the 2 valid ones
	if len(pool.proxies) != 2 {
		t.Errorf("expected 2 valid proxies, got %d", len(pool.proxies))
	}
}

func TestLoadProxyPoolInlineSingle(t *testing.T) {
	// Test inline single proxy (OPCODE_PROXY without file)
	pool, err := LoadProxyPool("<inline-single-proxy>", 5*time.Minute, false, "http://single-proxy:8080")
	if err != nil {
		t.Fatalf("LoadProxyPool inline failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool for inline proxy")
	}

	// Should have exactly 1 proxy
	if len(pool.proxies) != 1 {
		t.Errorf("expected 1 proxy, got %d", len(pool.proxies))
	}

	if got := pool.Current(); got != "http://single-proxy:8080" {
		t.Errorf("expected http://single-proxy:8080, got %s", got)
	}

	// Rotate should wrap back to the same proxy
	if got := pool.Rotate(); got != "http://single-proxy:8080" {
		t.Errorf("rotate should wrap to same proxy, got %s", got)
	}
}

func TestLoadProxyPoolInlineSingleEmpty(t *testing.T) {
	// Inline marker with empty proxy should return nil
	pool, err := LoadProxyPool("<inline-single-proxy>", 5*time.Minute, false, "")
	if err != nil {
		t.Errorf("expected no error for empty inline proxy, got %v", err)
	}
	if pool != nil {
		t.Error("expected nil pool for empty inline proxy")
	}
}

func TestClientForSessionCachesClient(t *testing.T) {
	tmpDir := t.TempDir()
	proxyFile := filepath.Join(tmpDir, "proxies.txt")
	content := "http://p1:8080\nhttp://p2:8080\nhttp://p3:8080\n"
	if err := os.WriteFile(proxyFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false, "")
	if err != nil {
		t.Fatalf("LoadProxyPool failed: %v", err)
	}

	// First call builds and caches; second returns the same pointer.
	c1, err := pool.ClientForSession("sess_alpha")
	if err != nil {
		t.Fatalf("first ClientForSession failed: %v", err)
	}
	// Identify which proxy index sess_alpha maps to by its URL.
	alphaURL := clientProxyURL(c1)
	idx := -1
	for i, u := range pool.proxies {
		if u == alphaURL {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("could not find proxy for session in pool: %q", alphaURL)
	}

	c2, err := pool.ClientForSession("sess_alpha")
	if err != nil {
		t.Fatalf("second ClientForSession failed: %v", err)
	}
	if c1 != c2 {
		t.Error("expected ClientForSession to return the same cached client pointer when healthy")
	}

	// A different session that maps to the same proxy must reuse the client.
	otherID := findSessionForIndex(pool, idx, "sess_alpha")
	c3, err := pool.ClientForSession(otherID)
	if err != nil {
		t.Fatalf("ClientForSession for other session failed: %v", err)
	}
	if c3 != c1 {
		t.Errorf("expected different session hitting same proxy (idx %d) to reuse the cached client; got distinct pointers", idx)
	}

	// MarkFailure invalidates the cache: a new call must build a fresh client.
	pool.MarkFailure(alphaURL, 503, true, "")
	c4, err := pool.ClientForSession("sess_alpha")
	if err != nil {
		t.Fatalf("ClientForSession after failure failed: %v", err)
	}
	if c4 == c1 {
		t.Error("expected cached client to be invalidated after MarkFailure")
	}
}

// clientProxyURL returns the proxy URL a client's transport is bound to,
// or "" if it dials directly (no proxy).
func clientProxyURL(c *http.Client) string {
	if c == nil || c.Transport == nil {
		return ""
	}
	t, ok := c.Transport.(*http.Transport)
	if !ok || t.Proxy == nil {
		return ""
	}
	u, _ := t.Proxy(nil)
	if u == nil {
		return ""
	}
	return u.String()
}

// preferredIndex returns the pool index ClientForSession would prefer for id.
func preferredIndex(p *ProxyPool, id string) int {
	h := fnv.New64a()
	h.Write([]byte(id))
	return int(h.Sum64() % uint64(len(p.proxies)))
}

// findSessionForIndex scans candidate IDs until it finds one whose preferred
// proxy index matches targetIdx and whose ID differs from avoid.
func findSessionForIndex(p *ProxyPool, targetIdx int, avoid string) string {
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("sess_alias_%d", i)
		if id == avoid {
			continue
		}
		if preferredIndex(p, id) == targetIdx {
			return id
		}
	}
	return avoid
}

func TestInlineSingleProxyClientForSession(t *testing.T) {
	// Regression: the inline single-proxy path failed to initialize
	// cooldownUntil, so ClientForSession panicked with "index out of range"
	// on p.cooldownUntil[0]. Must not panic.
	pool, err := LoadProxyPool("<inline-single-proxy>", 5*time.Minute, false, "http://single-proxy:8080")
	if err != nil {
		t.Fatalf("LoadProxyPool inline failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool for inline proxy")
	}

	client, err := pool.ClientForSession("sess_test_abc")
	if err != nil {
		t.Fatalf("ClientForSession failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	// Exercise fallback loop and MarkFailure/MarkSuccess too.
	pool.MarkFailure("http://single-proxy:8080", 0, true, "")
	client, err = pool.ClientForSession("sess_test_abc")
	if err != nil {
		t.Fatalf("ClientForSession after failure failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client after failure")
	}
	pool.MarkSuccess("http://single-proxy:8080")
}
