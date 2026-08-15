package reasoning

import (
	"encoding/json"
	"testing"
)

// A tool message's tool_call_id must survive the reasoning round-trip.
// Regression: reasoning.Message had no ToolCallID field, so unmarshal +
// marshal silently dropped it and upstream rejected the request with
// "messages[N]: missing field `tool_call_id`".
func TestInjectPreservesToolCallID(t *testing.T) {
	input := `{
		"model": "deepseek-v4-flash-free",
		"stream": true,
		"messages": [
			{"role": "user", "content": "what is the weather"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_abc", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}
			]},
			{"role": "tool", "content": "sunny", "tool_call_id": "call_abc"},
			{"role": "assistant", "content": "It is sunny."}
		]
	}`

	var body ChatBody
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	out := InjectReasoningContent("deepseek-v4-flash-free", &body)
	if out == nil {
		t.Fatal("InjectReasoningContent returned nil")
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var result struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("unmarshal result failed: %v", err)
	}

	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result.Messages))
	}

	toolMsg := result.Messages[2]
	if toolMsg["role"] != "tool" {
		t.Fatalf("messages[2] role = %v, want tool", toolMsg["role"])
	}
	if got := toolMsg["tool_call_id"]; got != "call_abc" {
		t.Errorf("tool_call_id = %v, want call_abc (dropped by round-trip)", got)
	}

	// The assistant tool_calls entry must keep its id too.
	assistantMsg := result.Messages[1]
	calls, ok := assistantMsg["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		t.Fatalf("assistant tool_calls missing: %v", assistantMsg)
	}
	if id := calls[0].(map[string]any)["id"]; id != "call_abc" {
		t.Errorf("tool_calls[0].id = %v, want call_abc", id)
	}
}

// Unknown fields at both the message and top level must pass through.
func TestInjectPreservesUnknownFields(t *testing.T) {
	input := `{
		"model": "deepseek-v4-flash-free",
		"messages": [
			{"role": "tool", "content": "x", "tool_call_id": "call_1", "some_future_field": "keep me"}
		],
		"top_level_unknown": {"nested": 1}
	}`

	var body ChatBody
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	encoded, err := json.Marshal(InjectReasoningContent("deepseek-v4-flash-free", &body))
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("unmarshal result failed: %v", err)
	}

	if result["top_level_unknown"] == nil {
		t.Errorf("top_level_unknown dropped: %s", encoded)
	}

	msgs := result["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if msg["some_future_field"] != "keep me" {
		t.Errorf("some_future_field dropped: %v", msg)
	}
	if msg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id dropped: %v", msg)
	}
}
