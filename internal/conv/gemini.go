package conv

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GeminiParser parses Gemini chat session JSON into ConversationEvents.
//
// Gemini chat files are full JSON documents rewritten over time (not JSONL),
// so Parse incrementally buffers incoming lines until a full document can be
// decoded, then emits only unseen events.
type GeminiParser struct {
	agentName      string
	conversationID string
	buffer         []byte
	seenEventIDs   map[string]bool
	nextSynthetic  int64
	hasSnapshot    bool
}

// NewGeminiParser creates a new Gemini parser.
func NewGeminiParser(agentName, conversationID string) *GeminiParser {
	return &GeminiParser{
		agentName:      agentName,
		conversationID: conversationID,
		seenEventIDs:   make(map[string]bool),
	}
}

func (p *GeminiParser) Runtime() string { return "gemini" }

func (p *GeminiParser) Reset() {
	p.buffer = nil
	p.seenEventIDs = make(map[string]bool)
	p.nextSynthetic = 0
	p.hasSnapshot = false
}

type geminiSessionFile struct {
	SessionID   string          `json:"sessionId"`
	ProjectHash string          `json:"projectHash"`
	StartTime   string          `json:"startTime"`
	LastUpdated string          `json:"lastUpdated"`
	Messages    []geminiMessage `json:"messages"`
}

type geminiMessage struct {
	ID        string            `json:"id"`
	Timestamp string            `json:"timestamp"`
	Type      string            `json:"type"`
	Content   string            `json:"content"`
	Thoughts  []geminiThought   `json:"thoughts"`
	Tokens    *geminiTokenUsage `json:"tokens"`
	Model     string            `json:"model"`
	ToolCalls []geminiToolCall  `json:"toolCalls"`
}

type geminiThought struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
}

type geminiTokenUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Cached int `json:"cached"`
}

type geminiToolCall struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Args                   json.RawMessage   `json:"args"`
	Result                 []json.RawMessage `json:"result"`
	Status                 string            `json:"status"`
	Timestamp              string            `json:"timestamp"`
	ResultDisplay          string            `json:"resultDisplay"`
	DisplayName            string            `json:"displayName"`
	Description            string            `json:"description"`
	RenderOutputAsMarkdown bool              `json:"renderOutputAsMarkdown"`
}

// Parse consumes a single line and emits zero or more normalized events.
func (p *GeminiParser) Parse(raw []byte) ([]ConversationEvent, error) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return nil, nil
	}

	// Tailer may re-read from file start after truncation; treat a new opening
	// brace as the start of a fresh document while preserving seen IDs.
	if line == "{" && p.hasSnapshot {
		p.buffer = p.buffer[:0]
		p.hasSnapshot = false
	}

	// Safety valve for very large/invalid buffers.
	if len(p.buffer)+len(raw)+1 > MaxReReadFileSize {
		p.buffer = p.buffer[:0]
		p.hasSnapshot = false
	}

	p.buffer = append(p.buffer, raw...)
	p.buffer = append(p.buffer, '\n')

	var doc geminiSessionFile
	if err := json.Unmarshal(p.buffer, &doc); err != nil {
		// Not a full JSON document yet.
		return nil, nil
	}
	p.hasSnapshot = true

	var events []ConversationEvent
	for i, msg := range doc.Messages {
		events = append(events, p.eventsForMessage(msg, i)...)
	}

	return events, nil
}

func (p *GeminiParser) eventsForMessage(msg geminiMessage, index int) []ConversationEvent {
	baseID := strings.TrimSpace(msg.ID)
	if baseID == "" {
		baseID = p.syntheticID("message", index)
	}
	ts := parseGeminiTimestamp(msg.Timestamp)

	var events []ConversationEvent
	appendIfNew := func(e ConversationEvent) {
		if e.EventID == "" {
			e.EventID = p.syntheticID("event", index)
		}
		if p.seenEventIDs[e.EventID] {
			return
		}
		p.seenEventIDs[e.EventID] = true
		events = append(events, e)
	}

	switch msg.Type {
	case "user":
		text := strings.TrimSpace(msg.Content)
		if text != "" {
			appendIfNew(ConversationEvent{
				EventID:        baseID,
				Type:           EventUser,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      ts,
				Role:           "user",
				Content:        []ContentBlock{{Type: "text", Text: truncateContent(text)}},
				Runtime:        "gemini",
			})
		}

	case "gemini":
		for i, th := range msg.Thoughts {
			thinking := strings.TrimSpace(th.Description)
			if thinking == "" {
				thinking = strings.TrimSpace(th.Subject)
			}
			if thinking == "" {
				continue
			}
			metadata := map[string]any{}
			if th.Subject != "" {
				metadata["subject"] = th.Subject
			}
			appendIfNew(ConversationEvent{
				EventID:        fmt.Sprintf("%s:thought:%d", baseID, i),
				Type:           EventThinking,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      parseGeminiTimestamp(th.Timestamp),
				Role:           "assistant",
				Content:        []ContentBlock{{Type: "thinking", Text: truncateContent(thinking)}},
				Runtime:        "gemini",
				Metadata:       metadata,
			})
		}

		text := strings.TrimSpace(msg.Content)
		if text != "" {
			var usage *TokenUsage
			if msg.Tokens != nil {
				usage = &TokenUsage{
					InputTokens:  msg.Tokens.Input,
					OutputTokens: msg.Tokens.Output,
					CacheRead:    msg.Tokens.Cached,
				}
			}
			appendIfNew(ConversationEvent{
				EventID:        baseID,
				Type:           EventAssistant,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      ts,
				Role:           "assistant",
				Content:        []ContentBlock{{Type: "text", Text: truncateContent(text)}},
				Model:          msg.Model,
				Runtime:        "gemini",
				TokenUsage:     usage,
			})
		}

		for i, call := range msg.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				callID = fmt.Sprintf("%s:tool:%d", baseID, i)
			}
			callTS := parseGeminiTimestamp(call.Timestamp)
			if callTS.IsZero() {
				callTS = ts
			}

			appendIfNew(ConversationEvent{
				EventID:        callID + ":use",
				Type:           EventToolUse,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      callTS,
				Role:           "assistant",
				Content: []ContentBlock{{
					Type:     "tool_use",
					ToolName: call.Name,
					ToolID:   callID,
					Input:    normalizeRawJSON(string(call.Args)),
				}},
				Runtime: "gemini",
				Metadata: map[string]any{
					"status":      call.Status,
					"displayName": call.DisplayName,
				},
			})

			output := extractGeminiToolOutput(call.Result)
			if output == "" && call.ResultDisplay != "" {
				output = call.ResultDisplay
			}
			appendIfNew(ConversationEvent{
				EventID:        callID + ":result",
				Type:           EventToolResult,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      callTS,
				Role:           "tool",
				Content: []ContentBlock{{
					Type:     "tool_result",
					ToolName: call.Name,
					ToolID:   callID,
					Output:   truncateContent(output),
					IsError:  toolCallIsError(call),
				}},
				Runtime: "gemini",
				Metadata: map[string]any{
					"status":      call.Status,
					"displayName": call.DisplayName,
				},
			})
		}

	case "info":
		info := strings.TrimSpace(msg.Content)
		if info != "" {
			appendIfNew(ConversationEvent{
				EventID:        baseID,
				Type:           EventSystem,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      ts,
				Runtime:        "gemini",
				Content:        []ContentBlock{{Type: "text", Text: truncateContent(info)}},
				Metadata: map[string]any{
					"originalType": "info",
				},
			})
		}

	case "error":
		errText := strings.TrimSpace(msg.Content)
		if errText != "" {
			appendIfNew(ConversationEvent{
				EventID:        baseID,
				Type:           EventError,
				AgentName:      p.agentName,
				ConversationID: p.conversationID,
				Timestamp:      ts,
				Runtime:        "gemini",
				Content:        []ContentBlock{{Type: "text", Text: truncateContent(errText)}},
				Metadata: map[string]any{
					"originalType": "error",
				},
			})
		}

	default:
		appendIfNew(ConversationEvent{
			EventID:        baseID,
			Type:           EventSystem,
			AgentName:      p.agentName,
			ConversationID: p.conversationID,
			Timestamp:      ts,
			Runtime:        "gemini",
			Metadata: map[string]any{
				"originalType": msg.Type,
			},
		})
	}

	return events
}

func parseGeminiTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (p *GeminiParser) syntheticID(kind string, n int) string {
	p.nextSynthetic++
	return "gemini:" + kind + ":" + strconv.Itoa(n) + ":" + strconv.FormatInt(p.nextSynthetic, 10)
}

func toolCallIsError(call geminiToolCall) bool {
	status := strings.ToLower(strings.TrimSpace(call.Status))
	if status == "" {
		return false
	}
	return status != "success"
}

func extractGeminiToolOutput(result []json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}

	var parts []string
	for _, item := range result {
		if len(item) == 0 {
			continue
		}

		// Most common shape:
		// { "functionResponse": { "response": { "output": "..." } } }
		var fr struct {
			FunctionResponse *struct {
				Response struct {
					Output any `json:"output"`
				} `json:"response"`
			} `json:"functionResponse"`
		}
		if err := json.Unmarshal(item, &fr); err == nil && fr.FunctionResponse != nil {
			switch out := fr.FunctionResponse.Response.Output.(type) {
			case string:
				if out != "" {
					parts = append(parts, out)
				}
			case nil:
			default:
				if b, err := json.Marshal(out); err == nil {
					parts = append(parts, string(b))
				}
			}
			continue
		}

		parts = append(parts, string(item))
	}

	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
