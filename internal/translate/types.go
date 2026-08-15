package translate

import "encoding/json"

// OpenAI Chat Completions 格式
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// 保留所有其他字段
	raw map[string]json.RawMessage
}

func (r *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type Alias ChatCompletionRequest
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	// 先解析到 raw map
	if err := json.Unmarshal(data, &r.raw); err != nil {
		return err
	}

	// 再解析结构化字段
	return json.Unmarshal(data, aux)
}

func (r *ChatCompletionRequest) MarshalJSON() ([]byte, error) {
	// 先把 raw 复制一份
	output := make(map[string]json.RawMessage)
	for k, v := range r.raw {
		output[k] = v
	}

	// 覆盖结构化字段（这些字段可能被修改过）
	output["model"], _ = json.Marshal(r.Model)
	output["messages"], _ = json.Marshal(r.Messages)

	return json.Marshal(output)
}

type Message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content,omitempty"` // 可能是 string 或 []ContentBlock
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	Name         string          `json:"name,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
}

type ToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function ToolCallFunctionDef `json:"function"`
}

type ToolCallFunctionDef struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Claude Messages 格式
type ClaudeRequest struct {
	Model      string               `json:"model"`
	Messages   []ClaudeMessage      `json:"messages"`
	Tools      json.RawMessage      `json:"tools,omitempty"`
	ToolChoice json.RawMessage      `json:"tool_choice,omitempty"`
	MaxTokens  int                  `json:"max_tokens,omitempty"`
	raw        map[string]json.RawMessage
}

func (r *ClaudeRequest) UnmarshalJSON(data []byte) error {
	type Alias ClaudeRequest
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	if err := json.Unmarshal(data, &r.raw); err != nil {
		return err
	}

	return json.Unmarshal(data, aux)
}

func (r *ClaudeRequest) MarshalJSON() ([]byte, error) {
	output := make(map[string]json.RawMessage)
	for k, v := range r.raw {
		output[k] = v
	}

	if r.Model != "" {
		output["model"], _ = json.Marshal(r.Model)
	}
	if r.Messages != nil {
		output["messages"], _ = json.Marshal(r.Messages)
	}
	if r.Tools != nil {
		output["tools"] = r.Tools
	}
	if r.ToolChoice != nil {
		output["tool_choice"] = r.ToolChoice
	}
	if r.MaxTokens > 0 {
		output["max_tokens"], _ = json.Marshal(r.MaxTokens)
	}

	return json.Marshal(output)
}

type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string 或 []ClaudeContentBlock
}

type ClaudeContentBlock struct {
	Type string `json:"type"` // "text", "tool_use", "tool_result"

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID   string          `json:"tool_use_id,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"` // 递归，可以包含 text blocks
	IsError     bool            `json:"is_error,omitempty"`
}

// Claude Tool 定义
type ClaudeTool struct {
	Type        string          `json:"type,omitempty"` // "function" for OpenAI format
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Function    json.RawMessage `json:"function,omitempty"` // OpenAI format
}

type ClaudeToolChoice struct {
	Type string `json:"type"` // "auto", "any", "none", "tool"
	Name string `json:"name,omitempty"` // for type="tool"
}
