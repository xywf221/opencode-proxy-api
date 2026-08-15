package translate

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionRequest_PreserveFields(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 0.7,
		"max_tokens": 100,
		"stream": true
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	t.Logf("Model: %s", req.Model)
	t.Logf("Messages: %d", len(req.Messages))
	t.Logf("Raw keys: %v", mapKeys(req.raw))

	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	t.Logf("Output: %s", string(out))

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output failed: %v", err)
	}

	if result["temperature"] == nil {
		t.Error("temperature missing")
	}
	if result["max_tokens"] == nil {
		t.Error("max_tokens missing")
	}
	if result["stream"] == nil {
		t.Error("stream missing")
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
