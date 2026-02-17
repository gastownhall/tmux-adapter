package conv

import (
	"strings"
	"testing"
	"time"
)

func TestCodexParserUserMessage(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	raw := []byte(`{"timestamp":"2026-02-17T04:17:02.935Z","type":"event_msg","payload":{"type":"user_message","message":"summarize"}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	e := events[0]
	if e.Type != EventUser {
		t.Fatalf("Type = %q, want %q", e.Type, EventUser)
	}
	if e.Role != "user" {
		t.Fatalf("Role = %q, want %q", e.Role, "user")
	}
	if e.Runtime != "codex" {
		t.Fatalf("Runtime = %q, want %q", e.Runtime, "codex")
	}
	if len(e.Content) != 1 || e.Content[0].Text != "summarize" {
		t.Fatalf("Content = %#v, want text summarize", e.Content)
	}
}

func TestCodexParserAssistantMessage(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	raw := []byte(`{"timestamp":"2026-02-17T04:17:04.840Z","type":"event_msg","payload":{"type":"agent_message","message":"Working on it."}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	e := events[0]
	if e.Type != EventAssistant {
		t.Fatalf("Type = %q, want %q", e.Type, EventAssistant)
	}
	if e.Role != "assistant" {
		t.Fatalf("Role = %q, want assistant", e.Role)
	}
	if len(e.Content) != 1 || e.Content[0].Text != "Working on it." {
		t.Fatalf("Content = %#v, want assistant text", e.Content)
	}
}

func TestCodexParserThinkingMessage(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	raw := []byte(`{"timestamp":"2026-02-17T04:17:18.515Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"**Planning next step**"}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Type != EventThinking {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventThinking)
	}
	if len(events[0].Content) != 1 || events[0].Content[0].Type != "thinking" {
		t.Fatalf("Content = %#v, want thinking block", events[0].Content)
	}
}

func TestCodexParserFunctionCallAndOutput(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	callRaw := []byte(`{"timestamp":"2026-02-17T04:17:15.004Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}","call_id":"call_123"}}`)
	callEvents, err := parser.Parse(callRaw)
	if err != nil {
		t.Fatalf("Parse(call) error = %v", err)
	}
	if len(callEvents) != 1 {
		t.Fatalf("len(callEvents) = %d, want 1", len(callEvents))
	}
	if callEvents[0].Type != EventToolUse {
		t.Fatalf("Type = %q, want %q", callEvents[0].Type, EventToolUse)
	}
	if len(callEvents[0].Content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(callEvents[0].Content))
	}
	if callEvents[0].Content[0].ToolName != "exec_command" {
		t.Fatalf("ToolName = %q, want exec_command", callEvents[0].Content[0].ToolName)
	}
	if string(callEvents[0].Content[0].Input) != `{"cmd":"pwd"}` {
		t.Fatalf("Input = %s, want {\"cmd\":\"pwd\"}", string(callEvents[0].Content[0].Input))
	}

	outRaw := []byte(`{"timestamp":"2026-02-17T04:17:15.141Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_123","output":"Chunk ID: abc\nProcess exited with code 0\nOutput:\n/Users/csells\n"}}`)
	outEvents, err := parser.Parse(outRaw)
	if err != nil {
		t.Fatalf("Parse(output) error = %v", err)
	}
	if len(outEvents) != 1 {
		t.Fatalf("len(outEvents) = %d, want 1", len(outEvents))
	}
	if outEvents[0].Type != EventToolResult {
		t.Fatalf("Type = %q, want %q", outEvents[0].Type, EventToolResult)
	}
	if outEvents[0].Content[0].ToolName != "exec_command" {
		t.Fatalf("tool name = %q, want exec_command", outEvents[0].Content[0].ToolName)
	}
	if outEvents[0].Content[0].IsError {
		t.Fatal("IsError = true, want false")
	}
	if !strings.Contains(outEvents[0].Content[0].Output, "/Users/csells") {
		t.Fatalf("output = %q, want path", outEvents[0].Content[0].Output)
	}
}

func TestCodexParserCustomToolOutputErrorMetadata(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	callRaw := []byte(`{"timestamp":"2026-02-17T04:17:15.004Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"call_patch","input":"*** Begin Patch"}}`)
	_, err := parser.Parse(callRaw)
	if err != nil {
		t.Fatalf("Parse(call) error = %v", err)
	}

	outRaw := []byte(`{"timestamp":"2026-02-17T04:17:15.141Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_patch","output":"{\"output\":\"apply_patch verification failed\",\"metadata\":{\"exit_code\":1}}"}}`)
	events, err := parser.Parse(outRaw)
	if err != nil {
		t.Fatalf("Parse(output) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if !events[0].Content[0].IsError {
		t.Fatal("IsError = false, want true")
	}
	if events[0].Content[0].ToolName != "apply_patch" {
		t.Fatalf("ToolName = %q, want apply_patch", events[0].Content[0].ToolName)
	}
	if got := events[0].Metadata["exit_code"]; got != float64(1) {
		t.Fatalf("metadata.exit_code = %#v, want 1", got)
	}
}

func TestCodexParserTaskComplete(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	raw := []byte(`{"timestamp":"2026-02-17T04:17:34.687Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn_1"}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Type != EventTurnEnd {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTurnEnd)
	}
	if events[0].EventID != "turn_1" {
		t.Fatalf("EventID = %q, want turn_1", events[0].EventID)
	}
}

func TestCodexParserIgnoresSessionMeta(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	raw := []byte(`{"timestamp":"2026-02-17T03:36:29.625Z","type":"session_meta","payload":{"id":"sess_1","cwd":"/tmp"}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(events))
	}
}

func TestCodexParserMalformedJSON(t *testing.T) {
	parser := NewCodexParser("codex-agent", "codex:codex-agent:sess-1")

	events, err := parser.Parse([]byte(`{bad-json`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Type != EventError {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventError)
	}
	if events[0].Timestamp.Before(time.Now().Add(-5 * time.Second)) {
		t.Fatalf("Timestamp too old: %v", events[0].Timestamp)
	}
}
