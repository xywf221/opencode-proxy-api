package translate

import (
	"encoding/json"
	"testing"
)

func TestNormalizeChatCompletionRequest(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "developer role converted to system",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "developer", "content": "You are a helpful assistant"},
					{"role": "user", "content": "Hello"}
				]
			}`,
			expected: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant"},
					{"role": "user", "content": "Hello"}
				]
			}`,
		},
		{
			name: "multiple developer roles",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "developer", "content": "Instruction 1"},
					{"role": "user", "content": "Hello"},
					{"role": "developer", "content": "Instruction 2"}
				]
			}`,
			expected: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "Instruction 1"},
					{"role": "user", "content": "Hello"},
					{"role": "system", "content": "Instruction 2"}
				]
			}`,
		},
		{
			name: "no developer role - unchanged",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are helpful"},
					{"role": "user", "content": "Hello"}
				]
			}`,
			expected: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are helpful"},
					{"role": "user", "content": "Hello"}
				]
			}`,
		},
		{
			name: "mixed roles preserved",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "System"},
					{"role": "developer", "content": "Dev"},
					{"role": "user", "content": "User"},
					{"role": "assistant", "content": "Assistant"},
					{"role": "tool", "content": "Tool", "tool_call_id": "123"}
				]
			}`,
			expected: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "System"},
					{"role": "system", "content": "Dev"},
					{"role": "user", "content": "User"},
					{"role": "assistant", "content": "Assistant"},
					{"role": "tool", "content": "Tool", "tool_call_id": "123"}
				]
			}`,
		},
		{
			name:     "empty messages - unchanged",
			input:    `{"model": "gpt-4", "messages": []}`,
			expected: `{"model": "gpt-4", "messages": []}`,
		},
		{
			name:     "no messages field - unchanged",
			input:    `{"model": "gpt-4"}`,
			expected: `{"model": "gpt-4"}`,
		},
		{
			name:     "invalid json - unchanged",
			input:    `{invalid json}`,
			expected: `{invalid json}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeChatCompletionRequest([]byte(tt.input))

			// For valid JSON, compare as objects to ignore whitespace
			var resultObj, expectedObj map[string]any
			if json.Unmarshal(result, &resultObj) == nil && json.Unmarshal([]byte(tt.expected), &expectedObj) == nil {
				resultJSON, _ := json.Marshal(resultObj)
				expectedJSON, _ := json.Marshal(expectedObj)
				if string(resultJSON) != string(expectedJSON) {
					t.Errorf("NormalizeChatCompletionRequest() =\n%s\nwant\n%s", string(resultJSON), string(expectedJSON))
				}
			} else {
				// For invalid JSON, compare as strings
				if string(result) != tt.expected {
					t.Errorf("NormalizeChatCompletionRequest() = %s, want %s", string(result), tt.expected)
				}
			}
		})
	}
}

func TestNormalizeChatCompletionRequest_PreservesOtherFields(t *testing.T) {
	input := `{
		"model": "gpt-4",
		"messages": [{"role": "developer", "content": "test"}],
		"temperature": 0.7,
		"max_tokens": 100,
		"stream": true
	}`

	result := NormalizeChatCompletionRequest([]byte(input))

	var resultObj map[string]any
	if err := json.Unmarshal(result, &resultObj); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Check that role was converted
	messages := resultObj["messages"].([]any)
	firstMsg := messages[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Errorf("Expected role 'system', got '%v'", firstMsg["role"])
	}

	// Check that other fields are preserved
	if resultObj["model"] != "gpt-4" {
		t.Errorf("model field not preserved")
	}
	if resultObj["temperature"] != 0.7 {
		t.Errorf("temperature field not preserved")
	}
	if resultObj["max_tokens"] != float64(100) {
		t.Errorf("max_tokens field not preserved")
	}
	if resultObj["stream"] != true {
		t.Errorf("stream field not preserved")
	}
}

func TestNormalizeChatCompletionRequest_GenerateToolCallID(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedCount int
		checkGenerated bool
	}{
		{
			name: "generate ID for empty tool_call_id",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "user", "content": "test"},
					{"role": "tool", "content": "result", "tool_call_id": ""}
				]
			}`,
			expectedCount: 2,
			checkGenerated: true,
		},
		{
			name: "generate ID for missing tool_call_id field",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "user", "content": "test"},
					{"role": "tool", "content": "result"}
				]
			}`,
			expectedCount: 2,
			checkGenerated: true,
		},
		{
			name: "keep existing tool_call_id",
			input: `{
				"model": "gpt-4",
				"messages": [
					{"role": "user", "content": "test"},
					{"role": "tool", "content": "result", "tool_call_id": "call_123"}
				]
			}`,
			expectedCount: 2,
			checkGenerated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := NormalizeChatCompletionRequest([]byte(tt.input))

			var result map[string]interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("Failed to unmarshal result: %v", err)
			}

			messages := result["messages"].([]interface{})
			if len(messages) != tt.expectedCount {
				t.Errorf("Expected %d messages, got %d", tt.expectedCount, len(messages))
			}

			// Find the tool message
			var toolMsg map[string]interface{}
			for _, msg := range messages {
				m := msg.(map[string]interface{})
				if m["role"] == "tool" {
					toolMsg = m
					break
				}
			}

			if toolMsg == nil {
				t.Fatal("Tool message not found")
			}

			toolCallID, ok := toolMsg["tool_call_id"].(string)
			if !ok || toolCallID == "" {
				t.Errorf("tool_call_id missing or empty: %v", toolMsg)
			}

			if tt.checkGenerated {
				// Should start with "call_"
				if len(toolCallID) < 6 || toolCallID[:5] != "call_" {
					t.Errorf("Generated tool_call_id should start with 'call_', got: %s", toolCallID)
				}
			}
		})
	}
}
