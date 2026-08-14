package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

const doublePipeDSML = "我来扫描当前目录。\n\n" +
	"<｜｜DSML｜｜tool_calls>\n" +
	"<｜｜DSML｜｜invoke name=\"Bash\">\n" +
	"<｜｜DSML｜｜parameter name=\"command\" string=\"true\">ls -la</｜｜DSML｜｜parameter>\n" +
	"<｜｜DSML｜｜parameter name=\"description\" string=\"true\">List contents</｜｜DSML｜｜parameter>\n" +
	"</｜｜DSML｜｜invoke>\n" +
	"</｜｜DSML｜｜tool_calls>"

const singlePipeDSML = "\n\n" +
	"<｜DSML｜tool_calls>\n" +
	"<｜DSML｜invoke name=\"shell_command\">\n" +
	"<｜DSML｜parameter name=\"command\" string=\"true\">ls -la</｜DSML｜parameter>\n" +
	"<｜DSML｜parameter name=\"description\" string=\"true\">List all files</｜DSML｜parameter>\n" +
	"</｜DSML｜invoke>\n" +
	"</｜DSML｜tool_calls>"

func TestStripAndParseDSMLDoublePipe(t *testing.T) {
	clean, calls, ok := StripAndParseDSML(doublePipeDSML)
	if !ok {
		t.Fatal("expected DSML parse ok")
	}
	if !strings.Contains(clean, "我来扫描当前目录") {
		t.Errorf("clean prose missing: %q", clean)
	}
	if strings.Contains(clean, "DSML") {
		t.Errorf("DSML leaked into clean text: %q", clean)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Name != "Bash" {
		t.Errorf("name = %q, want Bash", calls[0].Name)
	}
	if calls[0].Input["command"] != "ls -la" {
		t.Errorf("command = %#v", calls[0].Input["command"])
	}
}

func TestStripAndParseDSMLSinglePipeShellCommand(t *testing.T) {
	clean, calls, ok := StripAndParseDSML(singlePipeDSML)
	if !ok {
		t.Fatal("expected DSML parse ok")
	}
	if strings.Contains(clean, "DSML") {
		t.Errorf("DSML leaked: %q", clean)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	// shell_command must remap to Bash for Claude Code.
	if calls[0].Name != "Bash" {
		t.Errorf("name = %q, want Bash (remapped from shell_command)", calls[0].Name)
	}
	if calls[0].Input["command"] != "ls -la" {
		t.Errorf("command = %#v", calls[0].Input)
	}
}

func TestRewriteClaudeMessageDSML(t *testing.T) {
	in := map[string]interface{}{
		"id":    "msg_1",
		"type":  "message",
		"role":  "assistant",
		"model": "deepseek-v4-flash-free",
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": doublePipeDSML},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]interface{}{"input_tokens": 10, "output_tokens": 20},
	}
	raw, _ := json.Marshal(in)
	out := RewriteClaudeMessageDSML(raw)

	var msg map[string]interface{}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, out)
	}
	if msg["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", msg["stop_reason"])
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("content = %#v", msg["content"])
	}
	var sawText, sawTool bool
	for _, c := range content {
		b := c.(map[string]interface{})
		switch b["type"] {
		case "text":
			sawText = true
			if strings.Contains(b["text"].(string), "DSML") {
				t.Errorf("text still has DSML: %v", b["text"])
			}
		case "tool_use":
			sawTool = true
			if b["name"] != "Bash" {
				t.Errorf("tool name = %v", b["name"])
			}
			input := b["input"].(map[string]interface{})
			if input["command"] != "ls -la" {
				t.Errorf("input = %#v", input)
			}
			if b["id"] == "" {
				t.Error("missing tool id")
			}
		}
	}
	if !sawText || !sawTool {
		t.Errorf("sawText=%v sawTool=%v content=%#v", sawText, sawTool, content)
	}
}

func TestRewriteClaudeMessageDSMLNoop(t *testing.T) {
	in := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	out := RewriteClaudeMessageDSML(in)
	if string(out) != string(in) {
		t.Errorf("expected unchanged body, got %s", out)
	}
}

func TestRewriteClaudeStreamDSML(t *testing.T) {
	// Simulate opencode/Claude stream with text deltas carrying DSML.
	chunks := []string{
		`{"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","model":"deepseek-v4-flash-free","content":[],"usage":{"input_tokens":100,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"扫描中\n\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<｜DSML｜tool_calls>\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<｜DSML｜invoke name=\"Bash\">\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<｜DSML｜parameter name=\"command\" string=\"true\">pwd</｜DSML｜parameter>\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"</｜DSML｜invoke>\n</｜DSML｜tool_calls>"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}`,
		`{"type":"message_stop"}`,
	}
	var raw strings.Builder
	for _, c := range chunks {
		raw.WriteString("data: ")
		raw.WriteString(c)
		raw.WriteString("\n\n")
	}

	out := RewriteClaudeStreamDSML([]byte(raw.String()))
	s := string(out)
	if !strings.Contains(s, `"type":"tool_use"`) && !strings.Contains(s, `"type": "tool_use"`) {
		// json.Marshal has no spaces
		if !strings.Contains(s, `"tool_use"`) {
			t.Fatalf("expected tool_use in stream, got:\n%s", s)
		}
	}
	if !strings.Contains(s, `"stop_reason":"tool_use"`) {
		t.Errorf("expected stop_reason tool_use, got:\n%s", s)
	}
	if !strings.Contains(s, `"name":"Bash"`) {
		t.Errorf("expected Bash tool, got:\n%s", s)
	}
	if !strings.Contains(s, "pwd") {
		t.Errorf("expected command pwd in stream, got:\n%s", s)
	}
	if strings.Contains(s, "DSML") {
		t.Errorf("DSML should be stripped from rewritten stream:\n%s", s)
	}
	if !strings.Contains(s, "扫描中") {
		t.Errorf("prose text should be preserved:\n%s", s)
	}
}

func TestRewriteClaudeStreamDSMLNoop(t *testing.T) {
	in := []byte("data: {\"type\":\"message_stop\"}\n\n")
	out := RewriteClaudeStreamDSML(in)
	if string(out) != string(in) {
		t.Errorf("expected noop, got %s", out)
	}
}
