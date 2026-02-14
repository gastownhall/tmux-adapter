package conv

import (
	"bufio"
	"os"
	"testing"
)

func TestClaudeParserUserMessage(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello world"}]}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventUser {
		t.Fatalf("Type = %q, want %q", e.Type, EventUser)
	}
	if e.Role != "user" {
		t.Fatalf("Role = %q, want %q", e.Role, "user")
	}
	if e.EventID != "u1" {
		t.Fatalf("EventID = %q, want %q", e.EventID, "u1")
	}
	if len(e.Content) != 1 || e.Content[0].Text != "hello world" {
		t.Fatalf("Content = %+v, want text block with 'hello world'", e.Content)
	}
	if e.Runtime != "claude" {
		t.Fatalf("Runtime = %q, want %q", e.Runtime, "claude")
	}
}

func TestClaudeParserAssistantMessage(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"assistant","uuid":"a1","requestId":"req1","timestamp":"2026-02-14T01:45:00.362Z","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"Here is my response."}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventAssistant {
		t.Fatalf("Type = %q, want %q", e.Type, EventAssistant)
	}
	if e.Model != "claude-opus-4-6" {
		t.Fatalf("Model = %q, want %q", e.Model, "claude-opus-4-6")
	}
	if e.RequestID != "req1" {
		t.Fatalf("RequestID = %q, want %q", e.RequestID, "req1")
	}
	if e.TokenUsage == nil {
		t.Fatal("TokenUsage is nil")
	}
	if e.TokenUsage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", e.TokenUsage.InputTokens)
	}
	if e.TokenUsage.OutputTokens != 50 {
		t.Fatalf("OutputTokens = %d, want 50", e.TokenUsage.OutputTokens)
	}
}

func TestClaudeParserToolUse(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"assistant","uuid":"a2","timestamp":"2026-02-14T01:45:01.055Z","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventToolUse {
		t.Fatalf("Type = %q, want %q", e.Type, EventToolUse)
	}
	if e.Content[0].ToolName != "Read" {
		t.Fatalf("ToolName = %q, want %q", e.Content[0].ToolName, "Read")
	}
	if e.Content[0].ToolID != "toolu_123" {
		t.Fatalf("ToolID = %q, want %q", e.Content[0].ToolID, "toolu_123")
	}
}

func TestClaudeParserThinking(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"assistant","uuid":"a3","timestamp":"2026-02-14T01:44:59.309Z","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"thinking","thinking":"Let me think about this...","signature":"sig123"}]}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventThinking {
		t.Fatalf("Type = %q, want %q", e.Type, EventThinking)
	}
	if e.Content[0].Text != "Let me think about this..." {
		t.Fatalf("Text = %q, want thinking text", e.Content[0].Text)
	}
	if e.Content[0].Signature != "sig123" {
		t.Fatalf("Signature = %q, want %q", e.Content[0].Signature, "sig123")
	}
}

func TestClaudeParserToolResult(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"user","uuid":"u2","timestamp":"2026-02-14T01:45:01.076Z","message":{"role":"user","content":[{"tool_use_id":"toolu_123","type":"tool_result","content":"file contents here"}]}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventToolResult {
		t.Fatalf("Type = %q, want %q", e.Type, EventToolResult)
	}
	if e.Content[0].ToolID != "toolu_123" {
		t.Fatalf("ToolID = %q, want %q", e.Content[0].ToolID, "toolu_123")
	}
	if e.Content[0].Output != "file contents here" {
		t.Fatalf("Output = %q, want tool output", e.Content[0].Output)
	}
}

func TestClaudeParserProgress(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"progress","uuid":"p1","timestamp":"2026-02-14T01:44:54.307Z","data":{"type":"hook_progress","hookEvent":"SessionStart","hookName":"SessionStart:clear","command":"bd prime"}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventProgress {
		t.Fatalf("Type = %q, want %q", e.Type, EventProgress)
	}
	if e.Metadata["progressType"] != "hook_progress" {
		t.Fatalf("progressType = %v, want %q", e.Metadata["progressType"], "hook_progress")
	}
}

func TestClaudeParserQueueOp(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-02-14T01:44:54.458Z","sessionId":"abc","content":"background task completed"}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != EventQueueOp {
		t.Fatalf("Type = %q, want %q", e.Type, EventQueueOp)
	}
	if e.Metadata["operation"] != "enqueue" {
		t.Fatalf("operation = %v, want %q", e.Metadata["operation"], "enqueue")
	}
}

func TestClaudeParserFileHistorySnapshotSkipped(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0 (file-history-snapshot should be skipped)", len(events))
	}
}

func TestClaudeParserMalformedJSON(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{invalid json here`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v (should return error event, not error)", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 error event", len(events))
	}
	if events[0].Type != EventError {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventError)
	}
}

func TestClaudeParserUnknownType(t *testing.T) {
	parser := NewClaudeParser("test-agent", "claude:test-agent:abc123")

	raw := []byte(`{"type":"future-new-type","uuid":"f1","timestamp":"2026-02-14T01:44:54.253Z"}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 system event", len(events))
	}
	if events[0].Type != EventSystem {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventSystem)
	}
	if events[0].Metadata["originalType"] != "future-new-type" {
		t.Fatalf("originalType = %v, want %q", events[0].Metadata["originalType"], "future-new-type")
	}
}

func TestClaudeParserRealSamples(t *testing.T) {
	f, err := os.Open("testdata/claude/sample.jsonl")
	if err != nil {
		t.Skipf("test data not available: %v", err)
	}
	defer func() { _ = f.Close() }()

	parser := NewClaudeParser("test-agent", "claude:test-agent:sample")
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)

	lineNum := 0
	totalEvents := 0
	for scanner.Scan() {
		lineNum++
		events, err := parser.Parse(scanner.Bytes())
		if err != nil {
			t.Fatalf("line %d: Parse() error = %v", lineNum, err)
		}
		for _, e := range events {
			if e.Runtime != "claude" {
				t.Fatalf("line %d: Runtime = %q, want %q", lineNum, e.Runtime, "claude")
			}
			if e.AgentName != "test-agent" {
				t.Fatalf("line %d: AgentName = %q, want %q", lineNum, e.AgentName, "test-agent")
			}
		}
		totalEvents += len(events)
	}

	if lineNum == 0 {
		t.Fatal("no lines read from sample file")
	}
	if totalEvents == 0 {
		t.Fatal("no events parsed from sample file")
	}
}
