package conv

import (
	"encoding/json"
	"fmt"
	"time"
)

// ClaudeParser parses Claude Code JSONL lines into ConversationEvents.
type ClaudeParser struct {
	agentName      string
	conversationID string
}

// NewClaudeParser creates a new Claude Code parser.
func NewClaudeParser(agentName, conversationID string) *ClaudeParser {
	return &ClaudeParser{
		agentName:      agentName,
		conversationID: conversationID,
	}
}

func (p *ClaudeParser) Runtime() string { return "claude" }
func (p *ClaudeParser) Reset()          {}

// claudeRawLine is the top-level structure of a Claude Code JSONL line.
type claudeRawLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	RequestID   string          `json:"requestId"`
	CWD         string          `json:"cwd"`
	Message     json.RawMessage `json:"message"`
	Data        json.RawMessage `json:"data"`
	Operation   string          `json:"operation"`
	Content     string          `json:"content"`
	ToolUseID   string          `json:"toolUseID"`
	MessageID   string          `json:"messageId"`
}

// claudeMessage is the message envelope in assistant/user events.
type claudeMessage struct {
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	ID         string            `json:"id"`
	Content    json.RawMessage   `json:"content"`
	StopReason *string           `json:"stop_reason"`
	Usage      *claudeUsage      `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// claudeContentBlock represents a single content block in a message.
type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Caller    json.RawMessage `json:"caller"`
}

// claudeProgressData holds progress event data.
type claudeProgressData struct {
	Type      string `json:"type"`
	HookEvent string `json:"hookEvent"`
	HookName  string `json:"hookName"`
	Command   string `json:"command"`
}

// Parse converts a single Claude Code JSONL line into ConversationEvents.
func (p *ClaudeParser) Parse(raw []byte) ([]ConversationEvent, error) {
	var line claudeRawLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return []ConversationEvent{p.makeParseError(err, raw)}, nil
	}

	ts := p.parseTimestamp(line.Timestamp)
	eventID := line.UUID
	if eventID == "" {
		eventID = line.MessageID
	}

	switch line.Type {
	case "user":
		return p.parseUserMessage(line, ts, eventID)
	case "assistant":
		return p.parseAssistantMessage(line, ts, eventID)
	case "progress":
		return p.parseProgress(line, ts, eventID)
	case "queue-operation":
		return p.parseQueueOp(line, ts, eventID)
	case "file-history-snapshot":
		return nil, nil // skip
	default:
		return []ConversationEvent{p.makeSystemEvent(line.Type, ts, eventID, raw)}, nil
	}
}

func (p *ClaudeParser) parseUserMessage(line claudeRawLine, ts time.Time, eventID string) ([]ConversationEvent, error) {
	if line.Message == nil {
		return nil, nil
	}

	var msg claudeMessage
	if err := json.Unmarshal(line.Message, &msg); err != nil {
		return []ConversationEvent{p.makeParseError(err, line.Message)}, nil
	}

	blocks, hasToolResult := p.parseContentBlocks(msg.Content)

	if hasToolResult {
		// User message containing tool_result — emit as tool_result event
		var events []ConversationEvent
		for _, block := range blocks {
			if block.Type == "tool_result" {
				events = append(events, ConversationEvent{
					EventID:        eventID,
					Type:           EventToolResult,
					AgentName:      p.agentName,
					ConversationID: p.conversationID,
					Timestamp:      ts,
					Role:           "user",
					Content:        []ContentBlock{block},
					Runtime:        "claude",
					RequestID:      line.RequestID,
					ParentEventID:  line.ParentUUID,
				})
			}
		}
		return events, nil
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           EventUser,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Role:           "user",
		Content:        blocks,
		Runtime:        "claude",
		ParentEventID:  line.ParentUUID,
	}}, nil
}

func (p *ClaudeParser) parseAssistantMessage(line claudeRawLine, ts time.Time, eventID string) ([]ConversationEvent, error) {
	if line.Message == nil {
		return nil, nil
	}

	var msg claudeMessage
	if err := json.Unmarshal(line.Message, &msg); err != nil {
		return []ConversationEvent{p.makeParseError(err, line.Message)}, nil
	}

	blocks, _ := p.parseContentBlocks(msg.Content)
	if len(blocks) == 0 {
		return nil, nil
	}

	// Determine event type based on content
	eventType := EventAssistant
	if len(blocks) == 1 {
		switch blocks[0].Type {
		case "tool_use":
			eventType = EventToolUse
		case "thinking":
			eventType = EventThinking
		}
	}

	var usage *TokenUsage
	if msg.Usage != nil {
		usage = &TokenUsage{
			InputTokens:  msg.Usage.InputTokens,
			OutputTokens: msg.Usage.OutputTokens,
			CacheRead:    msg.Usage.CacheReadInputTokens,
			CacheCreate:  msg.Usage.CacheCreationInputTokens,
		}
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           eventType,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Role:           "assistant",
		Content:        blocks,
		Model:          msg.Model,
		Runtime:        "claude",
		TokenUsage:     usage,
		RequestID:      line.RequestID,
		ParentEventID:  line.ParentUUID,
	}}, nil
}

func (p *ClaudeParser) parseProgress(line claudeRawLine, ts time.Time, eventID string) ([]ConversationEvent, error) {
	var data claudeProgressData
	if line.Data != nil {
		_ = json.Unmarshal(line.Data, &data)
	}

	meta := map[string]any{}
	if data.Type != "" {
		meta["progressType"] = data.Type
	}
	if data.HookEvent != "" {
		meta["hookEvent"] = data.HookEvent
	}
	if data.HookName != "" {
		meta["hookName"] = data.HookName
	}
	if data.Command != "" {
		meta["command"] = data.Command
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           EventProgress,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Runtime:        "claude",
		Metadata:       meta,
		ParentEventID:  line.ParentUUID,
	}}, nil
}

func (p *ClaudeParser) parseQueueOp(line claudeRawLine, ts time.Time, eventID string) ([]ConversationEvent, error) {
	meta := map[string]any{
		"operation": line.Operation,
	}
	if line.Content != "" {
		meta["content"] = line.Content
	}

	return []ConversationEvent{{
		EventID:        eventID,
		Type:           EventQueueOp,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Runtime:        "claude",
		Metadata:       meta,
	}}, nil
}

// parseContentBlocks normalizes Claude message content (string or array) into ContentBlocks.
// Returns the blocks and whether any tool_result blocks were found.
func (p *ClaudeParser) parseContentBlocks(raw json.RawMessage) ([]ContentBlock, bool) {
	if raw == nil {
		return nil, false
	}

	// Try as string first
	var textContent string
	if json.Unmarshal(raw, &textContent) == nil {
		if textContent == "" {
			return nil, false
		}
		return []ContentBlock{{Type: "text", Text: truncateContent(textContent)}}, false
	}

	// Parse as array
	var rawBlocks []claudeContentBlock
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		return nil, false
	}

	var blocks []ContentBlock
	hasToolResult := false
	for _, rb := range rawBlocks {
		switch rb.Type {
		case "text":
			if rb.Text != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: truncateContent(rb.Text)})
			}
		case "thinking":
			blocks = append(blocks, ContentBlock{
				Type:      "thinking",
				Text:      truncateContent(rb.Thinking),
				Signature: rb.Signature,
			})
		case "tool_use":
			blocks = append(blocks, ContentBlock{
				Type:     "tool_use",
				ToolName: rb.Name,
				ToolID:   rb.ID,
				Input:    rb.Input,
			})
		case "tool_result":
			hasToolResult = true
			output := p.extractToolResultContent(rb.Content)
			blocks = append(blocks, ContentBlock{
				Type:   "tool_result",
				ToolID: rb.ToolUseID,
				Output: truncateContent(output),
			})
		}
	}
	return blocks, hasToolResult
}

func (p *ClaudeParser) extractToolResultContent(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}

	// Try as string
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}

	return string(raw)
}

func (p *ClaudeParser) parseTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Now()
	}
	return t
}

func (p *ClaudeParser) makeParseError(err error, _ []byte) ConversationEvent {
	return ConversationEvent{
		Type:           EventError,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      time.Now(),
		Runtime:        "claude",
		Content:        []ContentBlock{{Type: "text", Text: fmt.Sprintf("parse error: %v", err)}},
		Metadata: map[string]any{
			"errorKind": "parse",
		},
	}
}

func (p *ClaudeParser) makeSystemEvent(eventType string, ts time.Time, eventID string, _ []byte) ConversationEvent {
	return ConversationEvent{
		EventID:        eventID,
		Type:           EventSystem,
		AgentName:      p.agentName,
		ConversationID: p.conversationID,
		Timestamp:      ts,
		Runtime:        "claude",
		Metadata: map[string]any{
			"originalType": eventType,
		},
	}
}

func truncateContent(s string) string {
	if len(s) <= MaxContentSize {
		return s
	}
	return s[:MaxContentSize]
}
