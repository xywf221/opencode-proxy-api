package reasoning

import (
	"encoding/json"
	"strings"
)

// Models that need reasoning_content injected into assistant messages.
// These providers (DeepSeek, Kimi, etc.) require a non-empty placeholder
// on assistant messages or they reject the request.
var modelRules = []struct {
	match func(string) bool
	scope string // "all" or "toolCalls"
}{
	{match: func(m string) bool { return hasPrefixFold(m, "kimi-") }, scope: "toolCalls"},
	{match: func(m string) bool { return containsFold(m, "deepseek") }, scope: "all"},
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func containsFold(s, substr string) bool {
	s, substr = strings.ToLower(s), strings.ToLower(substr)
	return strings.Contains(s, substr)
}

// shouldInject checks if an assistant message needs a reasoning_content placeholder.
func shouldInject(role string, rc interface{}, toolCalls interface{}, scope string) bool {
	if role != "assistant" {
		return false
	}
	// Already has reasoning_content
	if s, ok := rc.(string); ok && len(s) > 0 {
		return false
	}
	if scope == "toolCalls" {
		tc, ok := toolCalls.([]interface{})
		return ok && len(tc) > 0
	}
	return true
}

// Message represents a single chat message in OpenAI format.
type Message struct {
	Role             string                 `json:"role"`
	Content          interface{}            `json:"content"`
	ReasoningContent interface{}            `json:"reasoning_content,omitempty"`
	ToolCalls        interface{}            `json:"tool_calls,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	Name             string                 `json:"name,omitempty"`
	raw              map[string]interface{} `json:"-"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type Alias Message
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	// Store all fields in raw map to preserve unknown fields
	m.raw = make(map[string]interface{})
	return json.Unmarshal(data, &m.raw)
}

func (m *Message) MarshalJSON() ([]byte, error) {
	output := make(map[string]interface{})
	for k, v := range m.raw {
		output[k] = v
	}
	// Overwrite with current struct values
	output["role"] = m.Role
	if m.Content != nil {
		output["content"] = m.Content
	}
	if m.ReasoningContent != nil {
		output["reasoning_content"] = m.ReasoningContent
	}
	if m.ToolCalls != nil {
		output["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != "" {
		output["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		output["name"] = m.Name
	}
	return json.Marshal(output)
}


// ChatBody is the OpenAI chat completion request body.
type ChatBody struct {
	Model               string                 `json:"model"`
	Messages            []Message              `json:"messages"`
	Stream              *bool                  `json:"stream,omitempty"`
	MaxTokens           *int                   `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                   `json:"max_completion_tokens,omitempty"`
	Temperature         *float64               `json:"temperature,omitempty"`
	TopP                *float64               `json:"top_p,omitempty"`
	Tools               []interface{}          `json:"tools,omitempty"`
	ToolChoice          interface{}            `json:"tool_choice,omitempty"`
	Stop                interface{}            `json:"stop,omitempty"`
	ExtraBody           map[string]interface{} `json:"extra_body,omitempty"`
	ReasoningEffort     interface{}            `json:"reasoning_effort,omitempty"`
	raw                 map[string]interface{} `json:"-"`
}

func (c *ChatBody) UnmarshalJSON(data []byte) error {
	type Alias ChatBody
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	// Store all fields in raw map to preserve unknown fields
	c.raw = make(map[string]interface{})
	return json.Unmarshal(data, &c.raw)
}

func (c *ChatBody) MarshalJSON() ([]byte, error) {
	output := make(map[string]interface{})
	for k, v := range c.raw {
		output[k] = v
	}
	// Overwrite with current struct values
	output["model"] = c.Model
	output["messages"] = c.Messages
	if c.Stream != nil {
		output["stream"] = c.Stream
	}
	if c.MaxTokens != nil {
		output["max_tokens"] = c.MaxTokens
	}
	if c.MaxCompletionTokens != nil {
		output["max_completion_tokens"] = c.MaxCompletionTokens
	}
	if c.Temperature != nil {
		output["temperature"] = c.Temperature
	}
	if c.TopP != nil {
		output["top_p"] = c.TopP
	}
	if c.Tools != nil {
		output["tools"] = c.Tools
	}
	if c.ToolChoice != nil {
		output["tool_choice"] = c.ToolChoice
	}
	if c.Stop != nil {
		output["stop"] = c.Stop
	}
	if c.ExtraBody != nil {
		output["extra_body"] = c.ExtraBody
	}
	if c.ReasoningEffort != nil {
		output["reasoning_effort"] = c.ReasoningEffort
	}
	return json.Marshal(output)
}


// InjectReasoningContent adds a reasoning_content placeholder to assistant
// messages when the model requires it (DeepSeek, Kimi, etc.).
func InjectReasoningContent(model string, body *ChatBody) *ChatBody {
	if body == nil || len(body.Messages) == 0 {
		return body
	}

	// Find matching rule
	var rule *struct {
		match func(string) bool
		scope string
	}
	for _, r := range modelRules {
		if r.match(model) {
			rule = &r
			break
		}
	}
	if rule == nil {
		return body
	}

	msgs := make([]Message, len(body.Messages))
	for i, m := range body.Messages {
		msgs[i] = m
		if shouldInject(m.Role, m.ReasoningContent, m.ToolCalls, rule.scope) {
			msgs[i].ReasoningContent = " "
		}
	}

	// Copy the original so unknown top-level fields (carried in raw) survive,
	// then swap in the rewritten messages.
	out := *body
	out.Messages = msgs
	return &out
}
