package sse

// Format represents different SSE/streaming response formats.
type Format string

const (
	// FormatZen is the upstream opencode.ai/zen format
	FormatZen Format = "zen"
	// FormatMessages is Anthropic Messages API format
	FormatMessages Format = "messages"
	// FormatChatCompletions is OpenAI Chat Completions format
	FormatChatCompletions Format = "chat_completions"
)

// TransformFunc is a function that transforms SSE event data.
// It receives the event type and data, and returns transformed data.
// Returning nil signals that the event should be skipped.
type TransformFunc func(eventType, data []byte) []byte

// ChainTransforms combines multiple transforms into one.
// Transforms are applied left-to-right. If any transform returns nil,
// the chain stops and returns nil (skip event).
func ChainTransforms(transforms ...TransformFunc) TransformFunc {
	return func(eventType, data []byte) []byte {
		result := data
		for _, fn := range transforms {
			if fn == nil {
				continue
			}
			result = fn(eventType, result)
			if result == nil {
				return nil
			}
		}
		return result
	}
}
