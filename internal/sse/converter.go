package sse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/xywf221/opencode-proxy-api/internal/translate"
)

// Converter is a streaming SSE transformer that applies transformations
// event-by-event without buffering the entire response.
type Converter struct {
	// SourceFormat is the format of the incoming stream (usually FormatZen).
	SourceFormat Format
	// TargetFormat is the format to convert to (ChatCompletions, Messages, etc).
	TargetFormat Format
	// RewriteDSML enables DeepSeek DSML tool call rewriting for Messages format.
	RewriteDSML bool
	// transforms are applied to each event in order.
	transforms []TransformFunc
}

func NewConverter(src, dst Format, opts ...ConverterOption) *Converter {
	c := &Converter{
		SourceFormat: src,
		TargetFormat: dst,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ConverterOption configures a Converter.
type ConverterOption func(*Converter)

// WithDSMLRewrite enables DSML tool call rewriting.
func WithDSMLRewrite(enable bool) ConverterOption {
	return func(c *Converter) {
		c.RewriteDSML = enable
	}
}

// WithTransform adds a custom transform function.
func WithTransform(fn TransformFunc) ConverterOption {
	return func(c *Converter) {
		c.transforms = append(c.transforms, fn)
	}
}

// Convert reads SSE events from src, transforms them, and writes to dst.
// For non-streaming responses (no SSE format), it buffers and transforms the whole body.
func (c *Converter) Convert(dst io.Writer, src io.Reader) (int64, error) {
	// Check if source is SSE format by peeking at first bytes
	buf := &bytes.Buffer{}
	tee := io.TeeReader(src, buf)

	peek := make([]byte, 256)
	n, _ := io.ReadFull(tee, peek)

	isSSE := bytes.Contains(peek[:n], []byte("data:")) ||
	        bytes.Contains(peek[:n], []byte("event:"))

	// Copy peeked data back
	fullSrc := io.MultiReader(buf, src)

	if !isSSE {
		// Not SSE - buffer entire body and transform as JSON
		return c.convertNonStreaming(dst, fullSrc)
	}

	// SSE stream - convert event by event
	return c.convertStreaming(dst, fullSrc)
}

// convertStreaming handles SSE event streams with per-event transformation.
func (c *Converter) convertStreaming(dst io.Writer, src io.Reader) (int64, error) {
	// For DSML rewrite, we need to buffer the entire stream first
	// because tool calls may be split across multiple deltas
	if c.RewriteDSML && c.TargetFormat == FormatMessages {
		raw, err := io.ReadAll(src)
		if err != nil {
			return 0, err
		}

		rewritten := translate.RewriteClaudeStreamDSML(raw)
		n, err := dst.Write(rewritten)
		return int64(n), err
	}

	// Stream-through mode: transform each event independently
	events, err := ReadAll(src)
	if err != nil {
		return 0, err
	}

	var written int64

	for _, event := range events {
		data := event.Data
		skip := false

		// Apply custom transforms
		for _, fn := range c.transforms {
			if fn == nil {
				continue
			}
			data = fn([]byte(event.Type), data)
			if data == nil {
				// Transform says skip this event
				skip = true
				break
			}
		}

		if skip {
			continue
		}

		// Write transformed event
		var buf bytes.Buffer
		tempWriter := NewWriter(&buf)
		if err := tempWriter.WriteEvent(event.Type, data); err != nil {
			return written, err
		}

		n, err := dst.Write(buf.Bytes())
		written += int64(n)
		if err != nil {
			return written, err
		}
	}

	return written, nil
}

// convertNonStreaming handles non-SSE JSON responses.
func (c *Converter) convertNonStreaming(dst io.Writer, src io.Reader) (int64, error) {
	body, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}

	// Apply DSML rewrite if enabled
	if c.RewriteDSML && c.TargetFormat == FormatMessages {
		body = translate.RewriteClaudeMessageDSML(body)
	}

	// Apply custom transforms (treat whole body as one event)
	for _, fn := range c.transforms {
		if fn == nil {
			continue
		}
		body = fn(nil, body)
		if body == nil {
			return 0, nil
		}
	}

	n, err := dst.Write(body)
	return int64(n), err
}

// DSMLRewriteTransform returns a transform function that rewrites DSML tool calls.
// This is a fallback for streaming mode when full buffering is not desired.
// Note: Full DSML rewrite requires complete stream, handled at Converter level.
func DSMLRewriteTransform() TransformFunc {
	return func(eventType, data []byte) []byte {
		// For now, pass through as-is
		// Full DSML rewrite requires complete stream, handled at higher level
		return data
	}
}

// ExtractJSONField extracts a JSON field from event data.
func ExtractJSONField(data []byte, field string) (interface{}, bool) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, false
	}
	val, ok := obj[field]
	return val, ok
}

// SetJSONField sets a JSON field in event data.
func SetJSONField(data []byte, field string, value interface{}) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return data, err
	}
	obj[field] = value
	return json.Marshal(obj)
}

// LogTransform returns a transform that logs each event (for debugging).
func LogTransform(logger func(eventType string, data []byte)) TransformFunc {
	return func(eventType, data []byte) []byte {
		if logger != nil {
			logger(string(eventType), data)
		}
		return data
	}
}

// FilterEventType returns a transform that only passes events of given types.
func FilterEventType(allowedTypes ...string) TransformFunc {
	allowed := make(map[string]bool)
	for _, t := range allowedTypes {
		allowed[t] = true
	}

	return func(eventType, data []byte) []byte {
		if len(allowed) == 0 {
			return data
		}
		if allowed[string(eventType)] {
			return data
		}
		return nil // Skip this event
	}
}

// MapEventType returns a transform that renames event types.
func MapEventType(mapping map[string]string) TransformFunc {
	return func(eventType, data []byte) []byte {
		// Note: this only transforms data, not the event type itself
		// To change event type, you'd need to modify the Writer call

		// This is a placeholder - actual implementation would need
		// access to modify the event type being written
		return data
	}
}

// Pretty formats a JSON event for readability (debugging).
func Pretty(data []byte) ([]byte, error) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return data, err
	}
	return json.MarshalIndent(obj, "", "  ")
}

// Compact removes whitespace from JSON event data.
func Compact(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return data, err
	}
	return buf.Bytes(), nil
}

// ValidateJSON returns a transform that only passes valid JSON events.
func ValidateJSON() TransformFunc {
	return func(eventType, data []byte) []byte {
		var obj interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			// Invalid JSON, skip
			return nil
		}
		return data
	}
}

// InjectField returns a transform that adds a JSON field to every event.
func InjectField(field string, value interface{}) TransformFunc {
	return func(eventType, data []byte) []byte {
		modified, err := SetJSONField(data, field, value)
		if err != nil {
			// If not JSON or error, return original
			return data
		}
		return modified
	}
}

// CountEvents returns a transform that counts events and logs every N events.
func CountEvents(every int, onCount func(count int)) TransformFunc {
	count := 0
	return func(eventType, data []byte) []byte {
		count++
		if every > 0 && count%every == 0 && onCount != nil {
			onCount(count)
		}
		return data
	}
}

// ErrorOnCondition returns a transform that returns an error if a condition is met.
func ErrorOnCondition(check func(data []byte) error) TransformFunc {
	return func(eventType, data []byte) []byte {
		if check != nil {
			if err := check(data); err != nil {
				// In a real implementation, we'd need a way to propagate errors
				// For now, just skip the event
				fmt.Printf("Error in transform: %v\n", err)
				return nil
			}
		}
		return data
	}
}
