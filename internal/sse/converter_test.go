package sse

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadAll(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantData []string
	}{
		{
			name: "single event",
			input: `data: {"type":"message_start"}

`,
			wantLen:  1,
			wantData: []string{`{"type":"message_start"}`},
		},
		{
			name: "multiple events",
			input: `data: {"type":"message_start"}

data: {"type":"content_block_delta"}

data: {"type":"message_stop"}

`,
			wantLen:  3,
			wantData: []string{`{"type":"message_start"}`, `{"type":"content_block_delta"}`, `{"type":"message_stop"}`},
		},
		{
			name: "multi-line data",
			input: `data: {"type":"message_start",
data: "model":"claude"}

`,
			wantLen:  1,
			wantData: []string{`{"type":"message_start",` + "\n" + `"model":"claude"}`},
		},
		{
			name: "with event type",
			input: `event: ping
data: {}

`,
			wantLen:  1,
			wantData: []string{`{}`},
		},
		{
			name:     "empty input",
			input:    "",
			wantLen:  0,
			wantData: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := ReadAll(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}

			if len(events) != tt.wantLen {
				t.Errorf("got %d events, want %d", len(events), tt.wantLen)
			}

			for i, want := range tt.wantData {
				if i >= len(events) {
					break
				}
				got := string(events[i].Data)
				if got != want {
					t.Errorf("event[%d].Data = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestWriter(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		data      string
		want      string
	}{
		{
			name:      "simple event",
			eventType: "",
			data:      `{"type":"test"}`,
			want:      "data: {\"type\":\"test\"}\n\n",
		},
		{
			name:      "with event type",
			eventType: "ping",
			data:      `{}`,
			want:      "event: ping\ndata: {}\n\n",
		},
		{
			name:      "multi-line data",
			eventType: "",
			data:      "line1\nline2\nline3",
			want:      "data: line1\ndata: line2\ndata: line3\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)

			err := w.WriteEvent(tt.eventType, []byte(tt.data))
			if err != nil {
				t.Fatalf("WriteEvent error: %v", err)
			}

			got := buf.String()
			if got != tt.want {
				t.Errorf("WriteEvent output:\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestConverter_PassThrough(t *testing.T) {
	input := `data: {"type":"message_start"}

data: {"type":"content_block_delta","delta":{"text":"Hello"}}

data: {"type":"message_stop"}

`

	var buf bytes.Buffer
	conv := NewConverter(FormatZen, FormatMessages)

	_, err := conv.Convert(&buf, strings.NewReader(input))
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}

	// Should pass through unchanged (no transforms applied)
	got := buf.String()
	if got != input {
		t.Errorf("Convert output changed:\ngot:  %q\nwant: %q", got, input)
	}
}

func TestConverter_WithTransform(t *testing.T) {
	input := `data: {"type":"test","value":1}

data: {"type":"test","value":2}

`

	// Transform that adds a field
	addField := func(eventType, data []byte) []byte {
		modified, _ := SetJSONField(data, "added", true)
		return modified
	}

	var buf bytes.Buffer
	conv := NewConverter(FormatZen, FormatMessages, WithTransform(addField))

	_, err := conv.Convert(&buf, strings.NewReader(input))
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, `"added":true`) {
		t.Errorf("Transform not applied, got: %s", got)
	}
}

func TestConverter_FilterEvents(t *testing.T) {
	input := `event: ping
data: {}

data: {"type":"message"}

event: ping
data: {}

`

	// Filter out ping events
	filter := func(eventType, data []byte) []byte {
		t.Logf("Filter called with eventType=%q, data=%q", string(eventType), string(data))
		if string(eventType) == "ping" {
			t.Logf("  -> Filtering out ping event")
			return nil
		}
		t.Logf("  -> Keeping event")
		return data
	}

	var buf bytes.Buffer
	conv := NewConverter(FormatZen, FormatMessages, WithTransform(filter))

	_, err := conv.Convert(&buf, strings.NewReader(input))
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}

	got := buf.String()
	t.Logf("Full output:\n%s", got)

	// Should only have one event (the message, not the pings)
	// Count complete events (data: line followed by empty line)
	events := strings.Split(strings.TrimSpace(got), "\n\n")
	nonEmptyEvents := 0
	for _, event := range events {
		if strings.Contains(event, "data:") && strings.TrimSpace(event) != "" {
			nonEmptyEvents++
			t.Logf("Event %d:\n%s", nonEmptyEvents, event)
		}
	}

	if nonEmptyEvents != 1 {
		t.Errorf("Filter failed: got %d events, want 1", nonEmptyEvents)
	}
}

func TestExtractJSONField(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		field string
		want  interface{}
		ok    bool
	}{
		{
			name:  "extract string",
			data:  `{"type":"test","value":"hello"}`,
			field: "value",
			want:  "hello",
			ok:    true,
		},
		{
			name:  "extract number",
			data:  `{"type":"test","count":42}`,
			field: "count",
			want:  float64(42),
			ok:    true,
		},
		{
			name:  "missing field",
			data:  `{"type":"test"}`,
			field: "missing",
			want:  nil,
			ok:    false,
		},
		{
			name:  "invalid json",
			data:  `not json`,
			field: "any",
			want:  nil,
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractJSONField([]byte(tt.data), tt.field)
			if ok != tt.ok {
				t.Errorf("ExtractJSONField ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ExtractJSONField = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetJSONField(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		field     string
		value     interface{}
		wantField string
		wantValue interface{}
	}{
		{
			name:      "set string",
			data:      `{"type":"test"}`,
			field:     "added",
			value:     "hello",
			wantField: "added",
			wantValue: "hello",
		},
		{
			name:      "override existing",
			data:      `{"type":"test","value":1}`,
			field:     "value",
			value:     2,
			wantField: "value",
			wantValue: float64(2),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := SetJSONField([]byte(tt.data), tt.field, tt.value)
			if err != nil {
				t.Fatalf("SetJSONField error: %v", err)
			}

			got, ok := ExtractJSONField(result, tt.wantField)
			if !ok {
				t.Errorf("Field %q not found in result", tt.wantField)
			}
			if got != tt.wantValue {
				t.Errorf("Field %q = %v, want %v", tt.wantField, got, tt.wantValue)
			}
		})
	}
}

func TestChainTransforms(t *testing.T) {
	// Create three transforms that each add a field
	add1 := func(eventType, data []byte) []byte {
		modified, _ := SetJSONField(data, "step1", true)
		return modified
	}
	add2 := func(eventType, data []byte) []byte {
		modified, _ := SetJSONField(data, "step2", true)
		return modified
	}
	add3 := func(eventType, data []byte) []byte {
		modified, _ := SetJSONField(data, "step3", true)
		return modified
	}

	chained := ChainTransforms(add1, add2, add3)

	input := []byte(`{"type":"test"}`)
	result := chained(nil, input)

	// All three fields should be present
	for _, field := range []string{"step1", "step2", "step3"} {
		if _, ok := ExtractJSONField(result, field); !ok {
			t.Errorf("Chained transform missing field: %s", field)
		}
	}
}

func TestChainTransforms_SkipOnNil(t *testing.T) {
	// First transform returns nil (skip event)
	skip := func(eventType, data []byte) []byte {
		return nil
	}
	// Second transform should never be called
	shouldNotRun := func(eventType, data []byte) []byte {
		t.Error("Second transform should not run after first returned nil")
		return data
	}

	chained := ChainTransforms(skip, shouldNotRun)

	input := []byte(`{"type":"test"}`)
	result := chained(nil, input)

	if result != nil {
		t.Errorf("Chained transform should return nil when first transform skips")
	}
}
