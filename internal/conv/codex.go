package conv

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// CodexParser parses Codex session JSONL lines into ConversationEvents.
type CodexParser struct {
	agentName      string
	conversationID string
	nextSynthetic  int64
	toolCalls      map[string]string // call_id -> tool name
}

// NewCodexParser creates a new Codex parser.
func NewCodexParser(agentName, conversationID string) *CodexParser {
	return &CodexParser{
		agentName:      agentName,
		conversationID: conversationID,
		toolCalls:      make(map[string]string),
	}
}

func (p *CodexParser) Runtime() string { return "codex" }

func (p *CodexParser) Reset() {
	p.nextSynthetic = 0
	p.toolCalls = make(map[string]string)
}

type codexRawLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexEventMsg struct {
	Type             string `json:"type"`
	Message          string `json:"message"`
	Text             string `json:"text"`
	TurnID           string `json:"turn_id"`
	LastAgentMessage string `json:"last_agent_message"`
}

type codexResponseItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
	Input     string `json:"input"`
	Output    string `json:"output"`
}

type codexStructuredToolOutput struct {
	Output   string         `json:"output"`
	Metadata map[string]any `json:"metadata"`
}

// Parse converts a single Codex JSONL line into ConversationEvents.
func (p *CodexParser) Parse(raw []byte) ([]ConversationEvent, error) {
	var line codexRawLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return []ConversationEvent{p.makeParseError(err)}, nil
	}

	ts := p.parseTimestamp(line.Timestamp)
	switch line.Type {
	case "event_msg":
		return p.parseEventMsg(line.Payload, ts)
	case "response_item":
		return p.parseResponseItem(line.Payload, ts)
	default:
		// Ignore high-noise/non-conversation records (session_meta, turn_context, compacted, etc.).
		return nil, nil
	}
}

func (p *CodexParser) parseEventMsg(raw json.RawMessage, ts time.Time) ([]ConversationEvent, error) {
	var msg codexEventMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return []ConversationEvent{p.makeParseError(err)}, nil
	}

	switch msg.Type {
	case "user_message":
		text := strings.TrimSpace(msg.Message)
		if text == "" {
			return nil, nil
		}
		return []ConversationEvent{{
			EventID:        p.syntheticID("user"),
			Type:           EventUser,
			AgentName:      p.agentName,
			ConversationID: p.conversationID,
			Timestamp:      ts,
			Role:           "user",
			Content:        []ContentBlock{{Type: "text", Text: truncateContent(text)}},
			Runtime:        "codex",
		}}, nil

	case "agent_message":
		text := strings.TrimSpace(msg.Message)
		if text == "" {
			return nil, nil
		}
		return []ConversationEvent{{
			EventID:        p.syntheticID("assistant"),
			Type:           EventAssistant,
			AgentName:      p.agentName,
			ConversationID: p.conversationID,
			Timestamp:      ts,
			Role:           "assistant",
			Content:        []ContentBlock{{Type: "text", Text: truncateContent(text)}},
			Runtime:        "codex",
		}}, nil

	case "agent_reasoning":
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return nil, nil
		}
		return []ConversationEvent{{
			EventID:        p.syntheticID("thinking"),
			Type:           EventThinking,
			AgentName:      p.agentName,
			ConversationID: p.conversationID,
			Timestamp:      ts,
			Role:           "assistant",
			Content:        []ContentBlock{{Type: "thinking", Text: truncateContent(text)}},
			Runtime:        "codex",
		}}, nil

	case "task_complete":
		eventID := msg.TurnID
		if eventID == "" {
			eventID = p.syntheticID("turn")
		}
		meta := map[string]any{}
		if msg.TurnID != "" {
			meta["turnId"] = msg.TurnID
		}
		return []ConversationEvent{{
			EventID:        eventID,
			Type:           EventTurnEnd,
			AgentName:      p.agentName,
			ConversationID: p.conversationID,
			Timestamp:      ts,
			Runtime:        "codex",
			Metadata:       meta,
		}}, nil

	default:
		// Skip task_started/token_count and other operational noise.
		return nil, nil
	}
}

func (p *CodexParser) parseResponseItem(raw json.RawMessage, ts time.Time) ([]ConversationEvent, error) {
	var item codexResponseItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return []ConversationEvent{p.makeParseError(err)}, nil
	}

	switch item.Type {
	case "function_call", "custom_tool_call":
		return p.parseToolUse(item, ts), nil
	case "function_call_output", "custom_tool_call_output":
		return p.parseToolResult(item, ts), nil
	default:
		// Ignore response_item message/reasoning/function_call_output duplicates not needed for conversation UI.
		return nil, nil
	}
}

func (p *CodexParser) parseToolUse(item codexResponseItem, ts time.Time) []ConversationEvent {
	eventID := item.CallID
	if eventID == "" {
		eventID = p.syntheticID("tool-use")
	}
	if item.CallID != "" && item.Name != "" {
		p.toolCalls[item.CallID] = item.Name
	}

	var inputRaw json.RawMessage
	if strings.TrimSpace(item.Arguments) != "" {
		inputRaw = normalizeRawJSON(item.Arguments)
	} else if strings.TrimSpace(item.Input) != "" {
		inputRaw = normalizeRawJSON(item.Input)
	}

	block := ContentBlock{
		Type:     "tool_use",
		ToolName: item.Name,
		ToolID:   item.CallID,
		Input:    inputRaw,
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           EventToolUse,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Role:           "assistant",
		Content:        []ContentBlock{block},
		Runtime:        "codex",
	}}
}

func (p *CodexParser) parseToolResult(item codexResponseItem, ts time.Time) []ConversationEvent {
	eventID := item.CallID
	if eventID == "" {
		eventID = p.syntheticID("tool-result")
	}

	toolName := p.toolCalls[item.CallID]
	delete(p.toolCalls, item.CallID)

	output, metadata := parseCodexToolOutput(item.Output)
	isErr := codexToolOutputIsError(output, metadata)

	block := ContentBlock{
		Type:     "tool_result",
		ToolName: toolName,
		ToolID:   item.CallID,
		Output:   truncateContent(output),
		IsError:  isErr,
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           EventToolResult,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Role:           "tool",
		Content:        []ContentBlock{block},
		Runtime:        "codex",
		Metadata:       metadata,
	}}
}

func normalizeRawJSON(s string) json.RawMessage {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	enc, err := json.Marshal(truncateContent(trimmed))
	if err != nil {
		return nil
	}
	return enc
}

func parseCodexToolOutput(raw string) (string, map[string]any) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	var structured codexStructuredToolOutput
	if err := json.Unmarshal([]byte(trimmed), &structured); err == nil && (structured.Output != "" || len(structured.Metadata) > 0) {
		if structured.Output != "" {
			return structured.Output, structured.Metadata
		}
		return trimmed, structured.Metadata
	}

	return trimmed, nil
}

func codexToolOutputIsError(output string, metadata map[string]any) bool {
	if code, ok := metadataExitCode(metadata); ok {
		return code != 0
	}

	lower := strings.ToLower(output)
	if strings.Contains(lower, "verification failed") {
		return true
	}

	// exec_command tool outputs this exact phrase when command fails.
	idx := strings.Index(lower, "process exited with code ")
	if idx < 0 {
		return false
	}
	codePart := lower[idx+len("process exited with code "):]
	fields := strings.Fields(codePart)
	if len(fields) == 0 {
		return false
	}
	code, err := strconv.Atoi(fields[0])
	if err != nil {
		return false
	}
	return code != 0
}

func metadataExitCode(metadata map[string]any) (int, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	v, ok := metadata["exit_code"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func (p *CodexParser) parseTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		log.Printf("codex: failed to parse timestamp %q: %v", ts, err)
		return time.Time{}
	}
	return t
}

func (p *CodexParser) syntheticID(kind string) string {
	p.nextSynthetic++
	return fmt.Sprintf("codex:%s:%d", kind, p.nextSynthetic)
}

func (p *CodexParser) makeParseError(err error) ConversationEvent {
	return ConversationEvent{
		Type:           EventError,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      time.Now(),
		Runtime:        "codex",
		Content:        []ContentBlock{{Type: "text", Text: fmt.Sprintf("parse error: %v", err)}},
		Metadata: map[string]any{
			"errorKind": "parse",
		},
	}
}
