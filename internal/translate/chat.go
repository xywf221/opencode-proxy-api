package translate

import (
	"encoding/json"
	"fmt"
	"time"
)

// NormalizeChatCompletionRequest rewrites OpenAI Chat Completions request
// to work with upstream providers that don't recognize newer role types.
// Currently handles:
//   - role: "developer" -> role: "system" (OpenAI added this in 2024)
//   - role: "tool" without valid tool_call_id -> generate one
func NormalizeChatCompletionRequest(body []byte) []byte {
	var req ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	if len(req.Messages) == 0 {
		return body
	}

	modified := false
	for i := range req.Messages {
		msg := &req.Messages[i]

		// Convert developer -> system for upstream compatibility
		if msg.Role == "developer" {
			msg.Role = "system"
			modified = true
		}

		// Generate tool_call_id for tool messages that are missing it
		if msg.Role == "tool" && msg.ToolCallID == "" {
			msg.ToolCallID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()/1000000, i)
			modified = true
		}
	}

	if !modified {
		return body
	}

	out, err := json.Marshal(&req)
	if err != nil {
		return body
	}
	return out
}
