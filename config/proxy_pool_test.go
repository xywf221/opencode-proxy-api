package config

import (
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

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false)
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

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false)
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
	pool, err := LoadProxyPool("", 5*time.Minute, false)
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

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false)
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

	pool, err := LoadProxyPool(proxyFile, 5*time.Minute, false)
	if err != nil {
		t.Fatalf("LoadProxyPool failed: %v", err)
	}

	// Should skip invalid lines and load only the 2 valid ones
	if len(pool.proxies) != 2 {
		t.Errorf("expected 2 valid proxies, got %d", len(pool.proxies))
	}
}
