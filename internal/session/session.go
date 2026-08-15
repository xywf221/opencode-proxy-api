package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
)

// IDs contains request-level identifiers for OpenCode headers.
type IDs struct {
	Session string // Stable per conversation
	Request string // Unique per request
	Project string // Randomized project ID
}

// ExtractFromRequest derives session IDs from the HTTP request and body.
// Priority:
//  1. Explicit headers: x-opencode-session, x-session-id, conversation-id
//  2. JSON metadata: metadata.session_id, conversation_id
//  3. Hash of first user message (for stable conversation tracking)
//  4. Random fallback
func ExtractFromRequest(r *http.Request, bodyBytes []byte) IDs {
	session := extractSessionID(r, bodyBytes)

	return IDs{
		Session: session,
		Request: generateUUID(),
		Project: randomProject(),
	}
}

// extractSessionID tries multiple sources for a stable session identifier.
func extractSessionID(r *http.Request, bodyBytes []byte) string {
	// Priority 1: Explicit headers
	headers := []string{
		"x-opencode-session",
		"x-session-id",
		"conversation-id",
		"conversationid",
	}
	for _, h := range headers {
		if val := strings.TrimSpace(r.Header.Get(h)); val != "" {
			return val
		}
	}

	// Priority 2: JSON body fields
	if len(bodyBytes) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err == nil {
			// metadata.session_id
			if metadata, ok := payload["metadata"].(map[string]any); ok {
				if sid, ok := metadata["session_id"].(string); ok && sid != "" {
					return sid
				}
			}
			// conversation_id or conversationId
			for _, key := range []string{"conversation_id", "conversationId"} {
				if val, ok := payload[key].(string); ok && val != "" {
					return val
				}
			}

			// Priority 3: Hash first user message
			if messages, ok := payload["messages"].([]any); ok && len(messages) > 0 {
				for _, msg := range messages {
					if m, ok := msg.(map[string]any); ok {
						if role := stringAt(m, "role"); role == "user" {
							if content := contentString(m["content"]); content != "" {
								return hashString(content)
							}
						}
					}
				}
			}
		}
	}

	// Priority 4: Random fallback
	return generateUUID()
}

// contentString extracts text from message content (string or content blocks).
func contentString(raw any) string {
	if s, ok := raw.(string); ok {
		return s
	}
	if blocks, ok := raw.([]any); ok {
		var parts []string
		for _, block := range blocks {
			if m, ok := block.(map[string]any); ok {
				if text := stringAt(m, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func stringAt(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// hashString creates a stable hex hash from input.
func hashString(s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return fmt.Sprintf("sess_%016x", h.Sum64())
}

// generateUUID creates a random UUID-like string.
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// randomProject generates a random project identifier.
func randomProject() string {
	prefixes := []string{
		"proj",
		"workspace",
		"app",
		"service",
		"client",
	}
	b := make([]byte, 8)
	rand.Read(b)
	idx := int(b[0]) % len(prefixes)
	return fmt.Sprintf("%s_%s", prefixes[idx], hex.EncodeToString(b[1:5]))
}

// OpencodeUserAgent returns a randomized OpenCode client user agent.
func OpencodeUserAgent() string {
	versions := []string{
		"0.9.12",
		"0.9.13",
		"0.9.14",
		"0.10.0",
		"0.10.1",
	}
	platforms := []string{
		"darwin-arm64",
		"darwin-amd64",
		"linux-amd64",
		"win32-x64",
	}
	b := make([]byte, 2)
	rand.Read(b)
	ver := versions[int(b[0])%len(versions)]
	plat := platforms[int(b[1])%len(platforms)]
	return fmt.Sprintf("OpenCode/%s (%s)", ver, plat)
}

// SessionHash computes a stable integer hash for session affinity.
func SessionHash(sessionID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(sessionID))
	return h.Sum64()
}
