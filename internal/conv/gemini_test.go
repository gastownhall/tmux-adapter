package conv

import (
	"encoding/json"
	"strings"
	"testing"
)

func feedGeminiDocument(t *testing.T, p *GeminiParser, doc any) []ConversationEvent {
	t.Helper()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}

	var all []ConversationEvent
	for _, line := range strings.Split(string(data), "\n") {
		events, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		all = append(all, events...)
	}
	return all
}

func TestGeminiParserBasicFlow(t *testing.T) {
	parser := NewGeminiParser("gem-agent", "gemini:gem-agent:s1")

	doc := map[string]any{
		"sessionId": "s1",
		"messages": []any{
			map[string]any{
				"id":        "u1",
				"timestamp": "2026-02-17T04:28:32.092Z",
				"type":      "user",
				"content":   "review this code",
			},
			map[string]any{
				"id":        "a1",
				"timestamp": "2026-02-17T04:28:40.000Z",
				"type":      "gemini",
				"content":   "I will inspect the files.",
				"thoughts": []any{
					map[string]any{
						"subject":     "Plan",
						"description": "Reading target files",
						"timestamp":   "2026-02-17T04:28:35.000Z",
					},
				},
				"tokens": map[string]any{
					"input":  10,
					"output": 20,
					"cached": 3,
				},
				"model": "gemini-3-pro-preview",
				"toolCalls": []any{
					map[string]any{
						"id":   "read_file-1",
						"name": "read_file",
						"args": map[string]any{
							"file_path": "README.md",
						},
						"status":    "success",
						"timestamp": "2026-02-17T04:28:36.000Z",
						"result": []any{
							map[string]any{
								"functionResponse": map[string]any{
									"response": map[string]any{
										"output": "file output",
									},
								},
							},
						},
					},
				},
			},
			map[string]any{
				"id":        "i1",
				"timestamp": "2026-02-17T04:28:41.000Z",
				"type":      "info",
				"content":   "update available",
			},
			map[string]any{
				"id":        "e1",
				"timestamp": "2026-02-17T04:28:42.000Z",
				"type":      "error",
				"content":   "permission denied",
			},
		},
	}

	events := feedGeminiDocument(t, parser, doc)
	if len(events) != 7 {
		t.Fatalf("len(events) = %d, want 7", len(events))
	}

	if events[0].Type != EventUser {
		t.Fatalf("events[0].Type = %q, want %q", events[0].Type, EventUser)
	}
	if events[1].Type != EventThinking {
		t.Fatalf("events[1].Type = %q, want %q", events[1].Type, EventThinking)
	}
	if events[2].Type != EventAssistant {
		t.Fatalf("events[2].Type = %q, want %q", events[2].Type, EventAssistant)
	}
	if events[2].Model != "gemini-3-pro-preview" {
		t.Fatalf("assistant model = %q, want gemini-3-pro-preview", events[2].Model)
	}
	if events[2].TokenUsage == nil || events[2].TokenUsage.InputTokens != 10 || events[2].TokenUsage.OutputTokens != 20 {
		t.Fatalf("assistant tokenUsage = %#v, want input=10 output=20", events[2].TokenUsage)
	}
	if events[3].Type != EventToolUse {
		t.Fatalf("events[3].Type = %q, want %q", events[3].Type, EventToolUse)
	}
	if events[4].Type != EventToolResult {
		t.Fatalf("events[4].Type = %q, want %q", events[4].Type, EventToolResult)
	}
	if events[4].Content[0].Output != "file output" {
		t.Fatalf("tool result output = %q, want file output", events[4].Content[0].Output)
	}
	if events[5].Type != EventSystem {
		t.Fatalf("events[5].Type = %q, want %q", events[5].Type, EventSystem)
	}
	if events[6].Type != EventError {
		t.Fatalf("events[6].Type = %q, want %q", events[6].Type, EventError)
	}
}

func TestGeminiParserDedupesOnRewrite(t *testing.T) {
	parser := NewGeminiParser("gem-agent", "gemini:gem-agent:s1")

	doc1 := map[string]any{
		"sessionId": "s1",
		"messages": []any{
			map[string]any{
				"id":        "u1",
				"timestamp": "2026-02-17T04:28:32.092Z",
				"type":      "user",
				"content":   "one",
			},
		},
	}

	first := feedGeminiDocument(t, parser, doc1)
	if len(first) != 1 {
		t.Fatalf("first len(events) = %d, want 1", len(first))
	}

	// Same document rewritten: should emit no duplicates.
	second := feedGeminiDocument(t, parser, doc1)
	if len(second) != 0 {
		t.Fatalf("second len(events) = %d, want 0", len(second))
	}

	// New message added: only new event should emit.
	doc2 := map[string]any{
		"sessionId": "s1",
		"messages": []any{
			map[string]any{
				"id":        "u1",
				"timestamp": "2026-02-17T04:28:32.092Z",
				"type":      "user",
				"content":   "one",
			},
			map[string]any{
				"id":        "u2",
				"timestamp": "2026-02-17T04:28:40.092Z",
				"type":      "user",
				"content":   "two",
			},
		},
	}

	third := feedGeminiDocument(t, parser, doc2)
	if len(third) != 1 {
		t.Fatalf("third len(events) = %d, want 1", len(third))
	}
	if third[0].EventID != "u2" {
		t.Fatalf("third[0].EventID = %q, want u2", third[0].EventID)
	}
}

func TestGeminiParserIncompleteJSONProducesNoEvents(t *testing.T) {
	parser := NewGeminiParser("gem-agent", "gemini:gem-agent:s1")

	events, err := parser.Parse([]byte("{"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0", len(events))
	}
}

func TestGeminiParserToolCallErrorStatus(t *testing.T) {
	parser := NewGeminiParser("gem-agent", "gemini:gem-agent:s1")

	doc := map[string]any{
		"sessionId": "s1",
		"messages": []any{
			map[string]any{
				"id":        "a1",
				"timestamp": "2026-02-17T04:28:40.000Z",
				"type":      "gemini",
				"content":   "failed tool",
				"toolCalls": []any{
					map[string]any{
						"id":     "tool-err",
						"name":   "exec_command",
						"status": "error",
						"args": map[string]any{
							"cmd": "false",
						},
						"result": []any{
							map[string]any{
								"functionResponse": map[string]any{
									"response": map[string]any{
										"output": "Process exited with code 1",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	events := feedGeminiDocument(t, parser, doc)
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (assistant/use/result)", len(events))
	}
	if events[2].Type != EventToolResult {
		t.Fatalf("events[2].Type = %q, want %q", events[2].Type, EventToolResult)
	}
	if !events[2].Content[0].IsError {
		t.Fatal("tool result IsError = false, want true")
	}
}
