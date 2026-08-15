package session

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractFromRequest_ExplicitHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"x-opencode-session", "x-opencode-session", "explicit-session-123"},
		{"x-session-id", "x-session-id", "session-456"},
		{"conversation-id", "conversation-id", "conv-789"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			req.Header.Set(tt.header, tt.value)

			ids := ExtractFromRequest(req, nil)
			if ids.Session != tt.value {
				t.Errorf("got session %q, want %q", ids.Session, tt.value)
			}
			if ids.Request == "" {
				t.Error("request ID should not be empty")
			}
			if ids.Project == "" {
				t.Error("project ID should not be empty")
			}
		})
	}
}

func TestExtractFromRequest_JSONMetadata(t *testing.T) {
	payload := map[string]any{
		"metadata": map[string]any{
			"session_id": "meta-session-abc",
		},
		"model": "claude-3-5-sonnet-20241022",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ids := ExtractFromRequest(req, body)

	if ids.Session != "meta-session-abc" {
		t.Errorf("got session %q, want %q", ids.Session, "meta-session-abc")
	}
}

func TestExtractFromRequest_ConversationID(t *testing.T) {
	payload := map[string]any{
		"conversation_id": "conv-xyz-789",
		"model":           "gpt-4",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ids := ExtractFromRequest(req, body)

	if ids.Session != "conv-xyz-789" {
		t.Errorf("got session %q, want %q", ids.Session, "conv-xyz-789")
	}
}

func TestExtractFromRequest_FirstUserMessageHash(t *testing.T) {
	payload := map[string]any{
		"model": "claude-3-5-sonnet-20241022",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant"},
			map[string]any{"role": "user", "content": "Hello, world!"},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ids1 := ExtractFromRequest(req, body)
	ids2 := ExtractFromRequest(req, body)

	// Same message should produce same session hash
	if ids1.Session != ids2.Session {
		t.Errorf("same message should produce stable session: %q vs %q", ids1.Session, ids2.Session)
	}
	if !strings.HasPrefix(ids1.Session, "sess_") {
		t.Errorf("hashed session should start with sess_, got %q", ids1.Session)
	}
}

func TestExtractFromRequest_ComplexContent(t *testing.T) {
	payload := map[string]any{
		"model": "claude-3-5-sonnet-20241022",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "Analyze this image"},
					map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/img.jpg"}},
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ids := ExtractFromRequest(req, body)

	// Should extract text from content blocks
	if !strings.HasPrefix(ids.Session, "sess_") {
		t.Errorf("should generate sess_ hash, got %q", ids.Session)
	}
}

func TestExtractFromRequest_RandomFallback(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ids1 := ExtractFromRequest(req, nil)
	ids2 := ExtractFromRequest(req, nil)

	// Without any session info, should generate random UUIDs (different each time)
	if ids1.Session == ids2.Session {
		t.Error("random fallback should generate different sessions")
	}
}

func TestGenerateUUID(t *testing.T) {
	uuid1 := generateUUID()
	uuid2 := generateUUID()

	if uuid1 == uuid2 {
		t.Error("UUIDs should be unique")
	}
	if len(uuid1) < 32 {
		t.Errorf("UUID too short: %q", uuid1)
	}
	if !strings.Contains(uuid1, "-") {
		t.Errorf("UUID should contain hyphens: %q", uuid1)
	}
}

func TestRandomProject(t *testing.T) {
	proj1 := randomProject()
	proj2 := randomProject()

	if proj1 == proj2 {
		t.Error("project IDs should be unique")
	}

	validPrefixes := []string{"proj_", "workspace_", "app_", "service_", "client_"}
	found := false
	for _, prefix := range validPrefixes {
		if strings.HasPrefix(proj1, prefix) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("project ID should start with valid prefix, got %q", proj1)
	}
}

func TestOpencodeUserAgent(t *testing.T) {
	ua1 := OpencodeUserAgent()

	if !strings.HasPrefix(ua1, "OpenCode/") {
		t.Errorf("user agent should start with OpenCode/, got %q", ua1)
	}
	if !strings.Contains(ua1, "(") || !strings.Contains(ua1, ")") {
		t.Errorf("user agent should contain platform, got %q", ua1)
	}

	// Should vary (though might occasionally match due to randomness)
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		seen[OpencodeUserAgent()] = true
	}
	if len(seen) < 2 {
		t.Error("user agent should vary across calls")
	}
}

func TestSessionHash(t *testing.T) {
	hash1 := SessionHash("session-abc")
	hash2 := SessionHash("session-abc")
	hash3 := SessionHash("session-xyz")

	if hash1 != hash2 {
		t.Error("same session should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different sessions should produce different hashes")
	}
}

func TestHeaderPriority(t *testing.T) {
	payload := map[string]any{
		"metadata": map[string]any{"session_id": "meta-session"},
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-session-id", "header-session")

	ids := ExtractFromRequest(req, body)

	// Header should win over metadata and message hash
	if ids.Session != "header-session" {
		t.Errorf("header should take priority, got %q", ids.Session)
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-session-id", "stable-session")

	ids1 := ExtractFromRequest(req, nil)
	ids2 := ExtractFromRequest(req, nil)

	// Same session, different request/project IDs
	if ids1.Session != ids2.Session {
		t.Error("session should be stable when header is set")
	}
	if ids1.Request == ids2.Request {
		t.Error("request IDs should be unique")
	}
	if ids1.Project == ids2.Project {
		t.Error("project IDs should be unique")
	}
}
