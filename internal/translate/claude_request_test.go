package translate

import (
	"encoding/json"
	"testing"
)

func TestRewriteAnthropicTools(t *testing.T) {
	in := []byte(`{
		"model":"deepseek-v4-flash-free",
		"max_tokens":64,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{
			"name":"add",
			"description":"Add two numbers",
			"input_schema":{"type":"object","properties":{"a":{"type":"number"}}}
		}]
	}`)
	out := ClaudeRequestToUpstream(in)
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, ok := raw["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", raw["tools"])
	}
	tool := tools[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("type = %v, want function", tool["type"])
	}
	fn, ok := tool["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing function: %#v", tool)
	}
	if fn["name"] != "add" {
		t.Errorf("name = %v, want add", fn["name"])
	}
	if _, ok := fn["parameters"]; !ok {
		t.Errorf("parameters missing: %#v", fn)
	}
	// messages still present
	if raw["messages"] == nil {
		t.Error("messages dropped")
	}
}

func TestRewriteToolChoice(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"add","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"add"}
	}`)
	out := ClaudeRequestToUpstream(in)
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tc, ok := raw["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice = %#v", raw["tool_choice"])
	}
	if tc["type"] != "function" {
		t.Errorf("type = %v", tc["type"])
	}
	fn := tc["function"].(map[string]interface{})
	if fn["name"] != "add" {
		t.Errorf("name = %v", fn["name"])
	}
}

func TestRewriteToolUseAndResult(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"add","input":{"a":1,"b":2}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"3"}
			]}
		]
	}`)
	out := ClaudeRequestToUpstream(in)
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := raw["messages"].([]interface{})
	if len(msgs) < 2 {
		t.Fatalf("messages len = %d, body=%s", len(msgs), string(out))
	}
	asst := msgs[0].(map[string]interface{})
	if asst["role"] != "assistant" {
		t.Errorf("role = %v", asst["role"])
	}
	tcs, ok := asst["tool_calls"].([]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls = %#v", asst["tool_calls"])
	}
	tc := tcs[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	if fn["name"] != "add" {
		t.Errorf("function.name = %v", fn["name"])
	}

	toolMsg := msgs[1].(map[string]interface{})
	if toolMsg["role"] != "tool" {
		t.Errorf("tool role = %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v", toolMsg["tool_call_id"])
	}
	if toolMsg["content"] != "3" {
		t.Errorf("content = %v", toolMsg["content"])
	}
}

func TestNoopWithoutTools(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":16}`)
	out := ClaudeRequestToUpstream(in)
	// unchanged bytes preferred
	if string(out) != string(in) {
		// allow semantic equality
		var a, b interface{}
		_ = json.Unmarshal(in, &a)
		_ = json.Unmarshal(out, &b)
		ai, _ := json.Marshal(a)
		bi, _ := json.Marshal(b)
		if string(ai) != string(bi) {
			t.Fatalf("unexpected rewrite:\n%s\n->\n%s", in, out)
		}
	}
}

func TestKeepOpenAITools(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"add","parameters":{"type":"object"}}}]
	}`)
	out := ClaudeRequestToUpstream(in)
	var raw map[string]interface{}
	_ = json.Unmarshal(out, &raw)
	tools := raw["tools"].([]interface{})
	tool := tools[0].(map[string]interface{})
	fn := tool["function"].(map[string]interface{})
	if fn["name"] != "add" {
		t.Errorf("name lost: %#v", fn)
	}
}

func TestSkipToolMessageWithoutCallID(t *testing.T) {
	input := `{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "test"},
			{"role": "assistant", "content": "response"},
			{"role": "tool", "content": "result1", "tool_call_id": ""},
			{"role": "tool", "content": "result2"}
		]
	}`

	out := ClaudeRequestToUpstream([]byte(input))

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	messages := result["messages"].([]interface{})
	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages (tool messages kept with generated IDs), got %d", len(messages))
	}

	// Tool messages must have a non-empty tool_call_id after rewrite.
	for i := 2; i < 4; i++ {
		msg := messages[i].(map[string]interface{})
		if msg["role"] != "tool" {
			t.Errorf("Expected role=tool, got %v", msg["role"])
		}
		if id, _ := msg["tool_call_id"].(string); id == "" {
			t.Errorf("tool message missing generated tool_call_id: %v", msg)
		}
	}
}

func TestClaudeRequestToUpstream_NeverEmptyMessages(t *testing.T) {
	// All messages are string-content tool messages without IDs. Previously
	// these were dropped, emptying the messages array and causing upstream
	// "Empty input messages". They must now be kept (with generated IDs).
	input := `{
		"model": "m",
		"messages": [
			{"role": "tool", "content": "r1"},
			{"role": "tool", "content": "r2"}
		]
	}`

	out := ClaudeRequestToUpstream([]byte(input))

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	messages, ok := result["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages missing after rewrite: %s", out)
	}
	if len(messages) == 0 {
		t.Fatalf("messages must not be empty after rewrite: %s", out)
	}
}

func TestSkipToolResultWithoutID(t *testing.T) {
	// Regression test: tool_result blocks without tool_use_id/tool_call_id
	// should be skipped to avoid upstream validation errors.
	in := []byte(`{
		"model":"m",
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"add","input":{"a":1}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","content":"result without ID"}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"valid result"}
			]}
		]
	}`)
	out := ClaudeRequestToUpstream(in)
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := raw["messages"].([]interface{})

	// Should have: assistant with tool_calls, tool message with valid ID
	// Should NOT have: tool message without ID
	toolMsgCount := 0
	for _, m := range msgs {
		msg := m.(map[string]interface{})
		if msg["role"] == "tool" {
			toolMsgCount++
			// Verify it has a valid tool_call_id
			if callID, ok := msg["tool_call_id"].(string); !ok || callID == "" {
				t.Errorf("tool message has empty tool_call_id: %#v", msg)
			}
		}
	}

	if toolMsgCount != 1 {
		t.Errorf("expected 1 tool message, got %d; body=%s", toolMsgCount, string(out))
	}
}
