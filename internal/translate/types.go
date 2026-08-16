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
