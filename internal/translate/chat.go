package translate

import (
	"encoding/json"
)

// NormalizeChatCompletionRequest rewrites OpenAI Chat Completions request
// to work with upstream providers that don't recognize newer role types.
// Currently handles:
//   - role: "developer" -> role: "system" (OpenAI added this in 2024)
func NormalizeChatCompletionRequest(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body // Return original on parse error
	}

	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body
	}

	modified := false
	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, ok := msgMap["role"].(string)
		if !ok {
			continue
		}
		// Convert developer -> system for upstream compatibility
		if role == "developer" {
			msgMap["role"] = "system"
			messages[i] = msgMap
			modified = true
		}
	}

	if !modified {
		return body
	}

	req["messages"] = messages
	out, err := json.Marshal(req)
	if err != nil {
		return body // Return original if re-marshal fails
	}
	return out
}
