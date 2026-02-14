package conv

import (
	"encoding/json"
	"time"
)

// Event types
const (
	EventUser       = "user"
	EventAssistant  = "assistant"
	EventSystem     = "system"
	EventToolUse    = "tool_use"
	EventToolResult = "tool_result"
	EventThinking   = "thinking"
	EventProgress   = "progress"
	EventTurnEnd    = "turn_end"
	EventQueueOp    = "queue_op"
	EventError      = "error"
)

// ConversationEvent is the universal event type streamed to clients.
// All runtimes (Claude, Codex, Gemini) normalize into this.
type ConversationEvent struct {
	Seq            int64     `json:"seq"`
	EventID        string    `json:"eventId"`
	GenerationID   string    `json:"generationId,omitempty"`
	Type           string    `json:"type"`
	AgentName      string    `json:"agentName"`
	ConversationID string    `json:"conversationId"`
	Timestamp      time.Time `json:"timestamp"`

	Role    string         `json:"role,omitempty"`
	Content []ContentBlock `json:"content,omitempty"`
	Model   string         `json:"model,omitempty"`

	Runtime       string         `json:"runtime"`
	TokenUsage    *TokenUsage    `json:"tokenUsage,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	ParentEventID string         `json:"parentEventId,omitempty"`
	SubagentID    string         `json:"subagentId,omitempty"`
	ParentConvID  string         `json:"parentConvId,omitempty"`
	DurationMs    int64          `json:"durationMs,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// ContentBlock is a normalized content element.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolName  string          `json:"toolName,omitempty"`
	ToolID    string          `json:"toolId,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    string          `json:"output,omitempty"`
	IsError   bool            `json:"isError,omitempty"`
	Signature string          `json:"signature,omitempty"`
	MimeType  string          `json:"mimeType,omitempty"`
	Data      string          `json:"data,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// TokenUsage tracks API token consumption.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	CacheRead    int `json:"cacheRead,omitempty"`
	CacheCreate  int `json:"cacheCreate,omitempty"`
}

// EventFilter controls which events a subscriber receives.
type EventFilter struct {
	Types           map[string]bool // nil = all types
	ExcludeThinking bool
	ExcludeProgress bool
}

// Matches returns true if the event passes the filter.
func (f EventFilter) Matches(e ConversationEvent) bool {
	if f.Types != nil {
		return f.Types[e.Type]
	}
	if f.ExcludeThinking && e.Type == EventThinking {
		return false
	}
	if f.ExcludeProgress && e.Type == EventProgress {
		return false
	}
	return true
}

// Cursor is an opaque resume token sent to clients.
type Cursor struct {
	ConversationID string `json:"c"`
	GenerationID   string `json:"g"`
	Seq            int64  `json:"s"`
	EventID        string `json:"e"`
}

// MaxContentSize is the maximum size in bytes for a single content block's text/output.
const MaxContentSize = 256 * 1024
