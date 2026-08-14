package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// DeepSeek free models often emit tool calls as DSML text instead of Claude
// tool_use blocks on /v1/messages. Observed tag shapes:
//
//	<｜｜DSML｜｜tool_calls> ... </｜｜DSML｜｜tool_calls>   (double fullwidth pipe)
//	<｜DSML｜tool_calls>   ... </｜DSML｜tool_calls>     (single fullwidth pipe)
//	<|DSML|tool_calls>     ... </|DSML|tool_calls>       (ASCII pipe dumps)
//
// Pipe run is ｜ (U+FF5C) or |, repeated 1–2 times on each side of DSML.

var (
	// Flexible open/close around DSML token.
	reDSMLToolCalls = regexp.MustCompile(
		`(?s)<(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})tool_calls>` +
			`(.*?)` +
			`</(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})tool_calls>`,
	)
	reDSMLInvoke = regexp.MustCompile(
		`(?s)<(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})invoke\s+name="([^"]+)"\s*>` +
			`(.*?)` +
			`</(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})invoke>`,
	)
	reDSMLParam = regexp.MustCompile(
		`(?s)<(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})parameter\s+name="([^"]+)"[^>]*>` +
			`(.*?)` +
			`</(?:｜{1,2}|\|{1,2})DSML(?:｜{1,2}|\|{1,2})parameter>`,
	)

	reExcessBlank = regexp.MustCompile(`\n{3,}`)
)

// DSMLCall is one parsed tool invocation from DSML markup.
type DSMLCall struct {
	Name  string
	Input map[string]interface{}
}

// ContainsDSML reports whether text has DeepSeek DSML tool-call markup.
func ContainsDSML(text string) bool {
	if !strings.Contains(text, "DSML") {
		return false
	}
	return strings.Contains(text, "tool_calls") || strings.Contains(text, "invoke")
}

// StripAndParseDSML removes DSML tool_calls regions from text and returns
// cleaned prose plus parsed tool calls. Returns ok=false when no DSML tools found.
func StripAndParseDSML(text string) (clean string, calls []DSMLCall, ok bool) {
	if !ContainsDSML(text) {
		return text, nil, false
	}

	calls = parseDSMLRegions(text, reDSMLToolCalls)
	if len(calls) == 0 {
		// Fallback: bare invoke tags outside a tool_calls wrapper.
		calls = parseInvokes(text)
	}
	if len(calls) == 0 {
		return text, nil, false
	}

	clean = reDSMLToolCalls.ReplaceAllString(text, "")
	clean = reDSMLInvoke.ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)
	clean = reExcessBlank.ReplaceAllString(clean, "\n\n")
	return clean, calls, true
}

func parseDSMLRegions(text string, reRegion *regexp.Regexp) []DSMLCall {
	var out []DSMLCall
	for _, m := range reRegion.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		out = append(out, parseInvokes(m[1])...)
	}
	return out
}

func parseInvokes(body string) []DSMLCall {
	var out []DSMLCall
	for _, m := range reDSMLInvoke.FindAllStringSubmatch(body, -1) {
		if len(m) < 3 {
			continue
		}
		name := mapToolName(strings.TrimSpace(m[1]))
		if name == "" {
			continue
		}
		input := map[string]interface{}{}
		for _, pm := range reDSMLParam.FindAllStringSubmatch(m[2], -1) {
			if len(pm) < 3 {
				continue
			}
			pkey := mapParamName(name, strings.TrimSpace(pm[1]))
			pval := strings.TrimSpace(pm[2])
			input[pkey] = pval
		}
		out = append(out, DSMLCall{Name: name, Input: input})
	}
	return out
}

// mapToolName remaps free-model internal names onto Claude Code tools.
func mapToolName(name string) string {
	switch strings.ToLower(name) {
	case "exec_command", "run_terminal_cmd", "shell", "bash_tool", "shell_command", "run_command":
		return "Bash"
	case "read_file", "read_files":
		return "Read"
	case "write_file", "create_file":
		return "Write"
	case "search_replace", "apply_patch", "edit_file":
		return "Edit"
	default:
		return name
	}
}

func mapParamName(tool, param string) string {
	p := strings.ToLower(param)
	switch strings.ToLower(tool) {
	case "bash":
		if p == "cmd" || p == "command_line" || p == "script" {
			return "command"
		}
	case "read":
		if p == "file" || p == "filepath" || p == "file_path" || p == "path" {
			return "file_path"
		}
	case "edit", "write":
		if p == "file" || p == "filepath" || p == "path" {
			return "file_path"
		}
	}
	return param
}

// RewriteClaudeMessageDSML converts DSML text inside a Claude Messages JSON
// response into proper tool_use content blocks. Unchanged body is returned as-is.
func RewriteClaudeMessageDSML(body []byte) []byte {
	if !ContainsDSML(string(body)) {
		return body
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	contentRaw, ok := raw["content"]
	if !ok {
		return body
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return body
	}

	var (
		outBlocks []map[string]interface{}
		calls     []DSMLCall
		changed   bool
		callIdx   int
	)
	for _, b := range blocks {
		typ, _ := b["type"].(string)
		if typ != "text" {
			outBlocks = append(outBlocks, b)
			continue
		}
		text, _ := b["text"].(string)
		clean, parsed, ok := StripAndParseDSML(text)
		if !ok {
			outBlocks = append(outBlocks, b)
			continue
		}
		changed = true
		calls = append(calls, parsed...)
		if strings.TrimSpace(clean) != "" {
			outBlocks = append(outBlocks, map[string]interface{}{
				"type": "text",
				"text": clean,
			})
		}
	}
	if !changed {
		return body
	}

	for _, c := range calls {
		callIdx++
		id := fmt.Sprintf("toolu_dsml_%d", callIdx)
		outBlocks = append(outBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    id,
			"name":  c.Name,
			"input": c.Input,
		})
	}

	newContent, err := json.Marshal(outBlocks)
	if err != nil {
		return body
	}
	raw["content"] = newContent
	raw["stop_reason"] = json.RawMessage(`"tool_use"`)

	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

// RewriteClaudeStreamDSML rewrites an Anthropic-style (or opencode NDJSON)
// messages stream that embedded DSML tool calls in text deltas into a proper
// tool_use SSE stream. If no DSML is present, raw is returned unchanged.
func RewriteClaudeStreamDSML(raw []byte) []byte {
	if !ContainsDSML(string(raw)) {
		return raw
	}

	text, model, msgID, usage := accumulateStreamMeta(raw)
	if strings.TrimSpace(text) == "" {
		// Fallback: whole body may be one JSON message (some gateways buffer).
		if rewritten := RewriteClaudeMessageDSML(raw); !bytes.Equal(rewritten, raw) {
			return messageJSONToSSE(rewritten)
		}
		// Last resort: parse DSML substrings directly from the wire bytes.
		text = string(raw)
	}

	clean, calls, ok := StripAndParseDSML(text)
	if !ok || len(calls) == 0 {
		// Body mentioned DSML but we could not parse invokes — leave as-is.
		return raw
	}
	return synthesizeToolUseSSE(msgID, model, clean, calls, usage)
}

type streamUsage struct {
	InputTokens  int
	OutputTokens int
}

func accumulateStreamMeta(raw []byte) (text, model, msgID string, usage streamUsage) {
	var b strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "{}" || line == "[DONE]" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if line == "" || line == "[DONE]" || line == "{}" {
				continue
			}
		}

		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		if t, _ := ev["type"].(string); t == "message_start" {
			if msg, ok := ev["message"].(map[string]interface{}); ok {
				if id, _ := msg["id"].(string); id != "" {
					msgID = id
				}
				if m, _ := msg["model"].(string); m != "" {
					model = m
				}
				extractUsage(msg["usage"], &usage)
			}
			continue
		}

		if delta, ok := ev["delta"].(map[string]interface{}); ok {
			if t, _ := delta["text"].(string); t != "" {
				b.WriteString(t)
			}
			// message_delta may carry stop/usage only.
			extractUsage(ev["usage"], &usage)
		}

		// Non-stream final payload mixed into a "stream".
		if content, ok := ev["content"].([]interface{}); ok {
			for _, c := range content {
				block, _ := c.(map[string]interface{})
				if block == nil {
					continue
				}
				if typ, _ := block["type"].(string); typ == "text" {
					if t, _ := block["text"].(string); t != "" {
						b.WriteString(t)
					}
				}
			}
			if m, _ := ev["model"].(string); m != "" {
				model = m
			}
			if id, _ := ev["id"].(string); id != "" {
				msgID = id
			}
			extractUsage(ev["usage"], &usage)
		}
	}
	return b.String(), model, msgID, usage
}

func extractUsage(v interface{}, usage *streamUsage) {
	m, ok := v.(map[string]interface{})
	if !ok || usage == nil {
		return
	}
	if n, ok := asInt(m["input_tokens"]); ok {
		usage.InputTokens = n
	}
	if n, ok := asInt(m["output_tokens"]); ok {
		usage.OutputTokens = n
	}
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case int:
		return n, true
	default:
		return 0, false
	}
}

func messageJSONToSSE(body []byte) []byte {
	var msg map[string]interface{}
	if err := json.Unmarshal(body, &msg); err != nil {
		return body
	}
	model, _ := msg["model"].(string)
	id, _ := msg["id"].(string)
	clean := ""
	var calls []DSMLCall
	if content, ok := msg["content"].([]interface{}); ok {
		for _, c := range content {
			block, _ := c.(map[string]interface{})
			if block == nil {
				continue
			}
			switch block["type"] {
			case "text":
				if t, _ := block["text"].(string); t != "" {
					if clean != "" {
						clean += "\n"
					}
					clean += t
				}
			case "tool_use":
				name, _ := block["name"].(string)
				input, _ := block["input"].(map[string]interface{})
				if input == nil {
					input = map[string]interface{}{}
				}
				calls = append(calls, DSMLCall{Name: name, Input: input})
			}
		}
	}
	var usage streamUsage
	extractUsage(msg["usage"], &usage)
	if len(calls) == 0 {
		return body
	}
	return synthesizeToolUseSSE(id, model, clean, calls, usage)
}

func synthesizeToolUseSSE(msgID, model, clean string, calls []DSMLCall, usage streamUsage) []byte {
	if msgID == "" {
		msgID = "msg_dsml_rewrite"
	}
	if model == "" {
		model = "unknown"
	}

	var buf bytes.Buffer
	writeSSE := func(v interface{}) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		buf.WriteString("data: ")
		buf.Write(b)
		buf.WriteString("\n\n")
	}

	writeSSE(map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":  usage.InputTokens,
				"output_tokens": 0,
			},
		},
	})

	index := 0
	if strings.TrimSpace(clean) != "" {
		writeSSE(map[string]interface{}{
			"type":  "content_block_start",
			"index": index,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})
		writeSSE(map[string]interface{}{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": clean,
			},
		})
		writeSSE(map[string]interface{}{
			"type":  "content_block_stop",
			"index": index,
		})
		index++
	}

	for i, c := range calls {
		id := fmt.Sprintf("toolu_dsml_%d", i+1)
		writeSSE(map[string]interface{}{
			"type":  "content_block_start",
			"index": index,
			"content_block": map[string]interface{}{
				"type":  "tool_use",
				"id":    id,
				"name":  c.Name,
				"input": map[string]interface{}{},
			},
		})
		argJSON, err := json.Marshal(c.Input)
		if err != nil {
			argJSON = []byte("{}")
		}
		writeSSE(map[string]interface{}{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": string(argJSON),
			},
		})
		writeSSE(map[string]interface{}{
			"type":  "content_block_stop",
			"index": index,
		})
		index++
	}

	outTokens := usage.OutputTokens
	if outTokens == 0 {
		outTokens = 1
	}
	writeSSE(map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   "tool_use",
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": outTokens,
		},
	})
	writeSSE(map[string]interface{}{
		"type": "message_stop",
	})

	return buf.Bytes()
}
