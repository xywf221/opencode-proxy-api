// Package translate adapts Anthropic Messages request fields that upstream
// still validates as OpenAI Chat Completions shapes (notably tools).
// Responses are not translated — /zen/v1/messages returns Claude format natively.
package translate

import (
	"encoding/json"
)

// ClaudeRequestToUpstream rewrites Anthropic-shaped tools / tool_choice /
// tool_use / tool_result into OpenAI-compatible fields expected by the
// opencode router, while keeping the rest of the Claude request intact.
// If the body is not JSON or has no Anthropic tools to rewrite, it is returned unchanged.
func ClaudeRequestToUpstream(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}

	changed := false

	if toolsRaw, ok := raw["tools"]; ok {
		if rewritten, ok := rewriteTools(toolsRaw); ok {
			raw["tools"] = rewritten
			changed = true
		}
	}

	if tcRaw, ok := raw["tool_choice"]; ok {
		if rewritten, ok := rewriteToolChoice(tcRaw); ok {
			raw["tool_choice"] = rewritten
			changed = true
		}
	}

	if msgsRaw, ok := raw["messages"]; ok {
		if rewritten, ok := rewriteMessages(msgsRaw); ok {
			raw["messages"] = rewritten
			changed = true
		}
	}

	if !changed {
		return body
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

func rewriteTools(raw json.RawMessage) (json.RawMessage, bool) {
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil || len(tools) == 0 {
		return nil, false
	}

	out := make([]map[string]interface{}, 0, len(tools))
	changed := false
	for _, t := range tools {
		// Already OpenAI-shaped: {type:"function", function:{name,...}}
		if fn, ok := t["function"]; ok {
			var f map[string]interface{}
			if json.Unmarshal(fn, &f) == nil {
				if name, _ := f["name"].(string); name != "" {
					// Keep as-is but ensure type is set.
					item := map[string]interface{}{
						"type":     "function",
						"function": f,
					}
					out = append(out, item)
					continue
				}
			}
		}

		// Anthropic-shaped: {name, description, input_schema}
		var name, desc string
		var schema json.RawMessage
		_ = json.Unmarshal(t["name"], &name)
		_ = json.Unmarshal(t["description"], &desc)
		if s, ok := t["input_schema"]; ok {
			schema = s
		} else if s, ok := t["parameters"]; ok {
			schema = s
		}
		if name == "" {
			// Unrecognized tool entry — pass through unchanged object.
			var passthrough map[string]interface{}
			_ = json.Unmarshal(mustMarshal(t), &passthrough)
			out = append(out, passthrough)
			continue
		}
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn := map[string]interface{}{
			"name":       name,
			"parameters": json.RawMessage(schema),
		}
		if desc != "" {
			fn["description"] = desc
		}
		out = append(out, map[string]interface{}{
			"type":     "function",
			"function": fn,
		})
		changed = true
	}

	if !changed {
		// All tools were already OpenAI-shaped.
		b, err := json.Marshal(out)
		if err != nil {
			return nil, false
		}
		// Still normalize type field; treat as change if bytes differ.
		if string(b) != string(raw) {
			return b, true
		}
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return b, true
}

func rewriteToolChoice(raw json.RawMessage) (json.RawMessage, bool) {
	// Already a plain string: "auto" / "required" / "none"
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return nil, false
	}

	var tc struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, false
	}

	switch tc.Type {
	case "auto":
		return json.RawMessage(`"auto"`), true
	case "any":
		return json.RawMessage(`"required"`), true
	case "none":
		return json.RawMessage(`"none"`), true
	case "tool":
		if tc.Name == "" {
			return nil, false
		}
		mapped, err := json.Marshal(map[string]interface{}{
			"type": "function",
			"function": map[string]string{
				"name": tc.Name,
			},
		})
		if err != nil {
			return nil, false
		}
		return mapped, true
	default:
		return nil, false
	}
}

func rewriteMessages(raw json.RawMessage) (json.RawMessage, bool) {
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) == 0 {
		return nil, false
	}

	out := make([]map[string]interface{}, 0, len(msgs)*2)
	changed := false

	for _, msg := range msgs {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		contentRaw := msg["content"]

		// Plain string content — leave alone (upstream sometimes accepts it).
		var contentStr string
		if json.Unmarshal(contentRaw, &contentStr) == nil {
			item := map[string]interface{}{
				"role":    role,
				"content": contentStr,
			}
			// Preserve extra fields.
			for k, v := range msg {
				if k == "role" || k == "content" {
					continue
				}
				var any interface{}
				if json.Unmarshal(v, &any) == nil {
					item[k] = any
				}
			}
			out = append(out, item)
			continue
		}

		var blocks []map[string]json.RawMessage
		if err := json.Unmarshal(contentRaw, &blocks); err != nil {
			// Unknown content shape — passthrough whole message.
			var passthrough map[string]interface{}
			_ = json.Unmarshal(mustMarshal(msg), &passthrough)
			out = append(out, passthrough)
			continue
		}

		var textParts []string
		var toolCalls []map[string]interface{}
		var toolResults []map[string]interface{}
		var otherBlocks []map[string]interface{}

		for _, b := range blocks {
			var typ string
			_ = json.Unmarshal(b["type"], &typ)
			switch typ {
			case "text":
				var text string
				_ = json.Unmarshal(b["text"], &text)
				textParts = append(textParts, text)
			case "tool_use":
				var id, name string
				_ = json.Unmarshal(b["id"], &id)
				_ = json.Unmarshal(b["name"], &name)
				args := "{}"
				if in, ok := b["input"]; ok && len(in) > 0 {
					args = string(in)
				}
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   id,
					"type": "function",
					"function": map[string]string{
						"name":      name,
						"arguments": args,
					},
				})
				changed = true
			case "tool_result":
				var toolUseID string
				if v, ok := b["tool_use_id"]; ok {
					_ = json.Unmarshal(v, &toolUseID)
				}
				if toolUseID == "" {
					if v, ok := b["tool_call_id"]; ok {
						_ = json.Unmarshal(v, &toolUseID)
					}
				}
				content := extractToolResultText(b)
				toolResults = append(toolResults, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": toolUseID,
					"content":      content,
				})
				changed = true
			default:
				var any map[string]interface{}
				_ = json.Unmarshal(mustMarshal(b), &any)
				otherBlocks = append(otherBlocks, any)
			}
		}

		if len(toolResults) > 0 {
			out = append(out, toolResults...)
			if len(textParts) > 0 || len(otherBlocks) > 0 {
				out = append(out, buildUserContentMessage(roleOrUser(role), textParts, otherBlocks))
			}
			continue
		}

		if len(toolCalls) > 0 {
			item := map[string]interface{}{
				"role":       "assistant",
				"tool_calls": toolCalls,
			}
			if len(textParts) == 1 && len(otherBlocks) == 0 {
				item["content"] = textParts[0]
			} else if len(textParts) > 0 || len(otherBlocks) > 0 {
				item["content"] = buildContentParts(textParts, otherBlocks)
			}
			out = append(out, item)
			continue
		}

		// No tool blocks — keep Claude content array (or collapse pure text).
		if len(otherBlocks) == 0 && len(textParts) == 1 {
			out = append(out, map[string]interface{}{
				"role":    role,
				"content": textParts[0],
			})
			// Collapsing content blocks to string is a mild normalize; only mark
			// changed when we actually rewrote tools elsewhere.
			continue
		}
		if len(otherBlocks) == 0 && len(textParts) > 1 {
			joined := ""
			for i, t := range textParts {
				if i > 0 {
					joined += "\n"
				}
				joined += t
			}
			out = append(out, map[string]interface{}{
				"role":    role,
				"content": joined,
			})
			continue
		}

		// Preserve original message when no tool rewrite happened for this msg.
		var passthrough map[string]interface{}
		_ = json.Unmarshal(mustMarshal(msg), &passthrough)
		out = append(out, passthrough)
	}

	if !changed {
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return b, true
}

func roleOrUser(role string) string {
	if role == "" {
		return "user"
	}
	return role
}

func buildUserContentMessage(role string, textParts []string, other []map[string]interface{}) map[string]interface{} {
	if len(other) == 0 {
		joined := ""
		for i, t := range textParts {
			if i > 0 {
				joined += "\n"
			}
			joined += t
		}
		return map[string]interface{}{"role": role, "content": joined}
	}
	return map[string]interface{}{
		"role":    role,
		"content": buildContentParts(textParts, other),
	}
}

func buildContentParts(textParts []string, other []map[string]interface{}) []interface{} {
	parts := make([]interface{}, 0, len(textParts)+len(other))
	for _, t := range textParts {
		parts = append(parts, map[string]interface{}{"type": "text", "text": t})
	}
	for _, o := range other {
		parts = append(parts, o)
	}
	return parts
}

func extractToolResultText(b map[string]json.RawMessage) string {
	src, ok := b["content"]
	if !ok || len(src) == 0 {
		src = b["input"]
	}
	if len(src) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(src, &s) == nil {
		return s
	}
	var inner []map[string]json.RawMessage
	if json.Unmarshal(src, &inner) == nil {
		var out string
		for i, block := range inner {
			var typ, text string
			_ = json.Unmarshal(block["type"], &typ)
			_ = json.Unmarshal(block["text"], &text)
			if typ == "text" || text != "" {
				if i > 0 && out != "" {
					out += "\n"
				}
				out += text
			}
		}
		return out
	}
	return string(src)
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
