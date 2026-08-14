package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseModelList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"single model", "deepseek-v4", 1},
		{"multiple models", "deepseek-v4,gpt-4,claude-3", 3},
		{"with whitespace", " deepseek-v4 , gpt-4 ", 2},
		{"empty entries", "deepseek-v4,,gpt-4,", 2},
		{"only commas", ",,,", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseModelList(tc.input)
			if tc.want == 0 {
				if got == nil {
					return
				}
				if len(got) != 0 {
					t.Errorf("parseModelList(%q) = %v (len=%d), want nil or empty map", tc.input, got, len(got))
				}
				return
			}
			if len(got) != tc.want {
				t.Errorf("parseModelList(%q) = %v, want %d entries", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsModelAllowed(t *testing.T) {
	tests := []struct {
		name    string
		models  map[string]struct{}
		model   string
		allowed bool
	}{
		{"nil map", nil, "anything", true},
		{"empty map", map[string]struct{}{}, "anything", true},
		{"model in list", map[string]struct{}{"deepseek-v4": {}}, "deepseek-v4", true},
		{"model not in list", map[string]struct{}{"gpt-4": {}}, "deepseek-v4", false},
		{"case sensitive", map[string]struct{}{"DeepSeek-v4": {}}, "deepseek-v4", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{AllowedModels: tc.models}
			if got := c.IsModelAllowed(tc.model); got != tc.allowed {
				t.Errorf("IsModelAllowed(%q) = %v, want %v", tc.model, got, tc.allowed)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	// Ensure env vars are clean
	os.Unsetenv("OPCODE_API_KEY")
	os.Unsetenv("OPCODE_LISTEN")
	os.Unsetenv("OPCODE_UPSTREAM_BASE")
	os.Unsetenv("OPCODE_UPSTREAM_TOKEN")
	os.Unsetenv("OPCODE_ALLOWED_MODELS")
	os.Unsetenv("OPCODE_UPSTREAM_TIMEOUT")
	os.Unsetenv("OPCODE_PROXY")

	c := Load()
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", c.ListenAddr)
	}
	if c.UpstreamBase != "https://opencode.ai" {
		t.Errorf("UpstreamBase = %q, want https://opencode.ai", c.UpstreamBase)
	}
	if c.UpstreamToken != "public" {
		t.Errorf("UpstreamToken = %q, want public", c.UpstreamToken)
	}
	if c.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", c.APIKey)
	}
	if c.AllowedModels != nil {
		t.Errorf("AllowedModels = %v, want nil", c.AllowedModels)
	}
	if c.UpstreamTimeout == 0 {
		t.Error("UpstreamTimeout should not be zero")
	}
	if c.ProxyURL != "" {
		t.Errorf("ProxyURL = %q, want empty", c.ProxyURL)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	os.Setenv("OPCODE_LISTEN", ":9999")
	os.Setenv("OPCODE_API_KEY", "test-key")
	os.Setenv("OPCODE_ALLOWED_MODELS", "a,b")
	os.Setenv("OPCODE_UPSTREAM_TIMEOUT", "30s")
	os.Setenv("OPCODE_PROXY", "socks5://127.0.0.1:1080")
	defer func() {
		os.Unsetenv("OPCODE_LISTEN")
		os.Unsetenv("OPCODE_API_KEY")
		os.Unsetenv("OPCODE_ALLOWED_MODELS")
		os.Unsetenv("OPCODE_UPSTREAM_TIMEOUT")
		os.Unsetenv("OPCODE_PROXY")
	}()

	c := Load()
	if c.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want :9999", c.ListenAddr)
	}
	if c.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want test-key", c.APIKey)
	}
	if len(c.AllowedModels) != 2 {
		t.Errorf("AllowedModels = %v, want 2 entries", c.AllowedModels)
	}
	if c.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Errorf("ProxyURL = %q, want socks5://127.0.0.1:1080", c.ProxyURL)
	}
}

func TestNewTransport(t *testing.T) {
	tests := []struct {
		name    string
		proxy   string
		wantErr bool
	}{
		{"empty", "", false},
		{"http", "http://127.0.0.1:8080", false},
		{"https", "https://proxy.example:8443", false},
		{"socks5", "socks5://127.0.0.1:1080", false},
		{"socks5h", "socks5h://127.0.0.1:1080", false},
		{"socks5 with auth", "socks5://user:pass@127.0.0.1:1080", false},
		{"http with auth", "http://user:pass@127.0.0.1:8080", false},
		{"unsupported scheme", "ftp://127.0.0.1:21", true},
		{"missing host", "socks5://", true},
		{"no scheme host only", "127.0.0.1:1080", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := newTransport(tc.proxy, false)
			if tc.wantErr {
				if err == nil {
					t.Errorf("newTransport(%q) expected error, got nil", tc.proxy)
				}
				return
			}
			if err != nil {
				t.Errorf("newTransport(%q) unexpected error: %v", tc.proxy, err)
				return
			}
			if rt == nil {
				t.Errorf("newTransport(%q) returned nil transport", tc.proxy)
			}
		})
	}
}

func TestRedactProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no credentials", "socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"user and password", "socks5h://grok:secret@1.2.3.4:7890", "socks5h://grok:***@1.2.3.4:7890"},
		{"user only", "http://grok@1.2.3.4:8080", "http://grok@1.2.3.4:8080"},
		{"empty password still masked", "socks5://grok:@1.2.3.4:1080", "socks5://grok:***@1.2.3.4:1080"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactProxyURL(tc.in)
			if got != tc.want {
				t.Errorf("RedactProxyURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactProxyURLNeverLeaksPassword(t *testing.T) {
	const password = "KQgYTkbMU3Mj3vJe"
	inputs := []string{
		"socks5h://grok:" + password + "@103.79.76.229:7890",
		"http://grok:" + password + "@1.2.3.4:8080",
		// Unparseable input must be reported, not echoed.
		"socks5://grok:" + password + "@ho st:1080/\x7f",
	}
	for _, in := range inputs {
		if got := RedactProxyURL(in); strings.Contains(got, password) {
			t.Errorf("RedactProxyURL(%q) leaked the password: %q", in, got)
		}
	}
}

func TestNewUpstreamClient(t *testing.T) {
	c := &Config{
		UpstreamTimeout: 30 * time.Second,
		ProxyURL:        "socks5h://127.0.0.1:1080",
	}
	client, err := c.NewUpstreamClient()
	if err != nil {
		t.Fatalf("NewUpstreamClient: %v", err)
	}
	if client == nil {
		t.Fatal("NewUpstreamClient returned nil client")
	}
	if client.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", client.Timeout)
	}
	if client.Transport == nil {
		t.Error("Transport should not be nil when proxy is set")
	}
}

func TestNewUpstreamClientDirect(t *testing.T) {
	c := &Config{UpstreamTimeout: time.Minute}
	client, err := c.NewUpstreamClient()
	if err != nil {
		t.Fatalf("NewUpstreamClient: %v", err)
	}
	if client.Timeout != time.Minute {
		t.Errorf("Timeout = %v, want 1m", client.Timeout)
	}
}
