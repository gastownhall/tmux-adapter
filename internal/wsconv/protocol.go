package wsconv

import (
	"encoding/json"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
)

// clientMessage is a JSON message received from a WebSocket client.
type clientMessage struct {
	ID                   string        `json:"id"`
	Type                 string        `json:"type"`
	Protocol             string        `json:"protocol,omitempty"`
	ConversationID       string        `json:"conversationId,omitempty"`
	Agent                string        `json:"agent,omitempty"`
	Prompt               string        `json:"prompt,omitempty"`
	SubscriptionID       string        `json:"subscriptionId,omitempty"`
	Filter               *clientFilter `json:"filter,omitempty"`
	Cursor               string        `json:"cursor,omitempty"`
	IncludeSessionFilter string        `json:"includeSessionFilter,omitempty"`
	ExcludeSessionFilter string        `json:"excludeSessionFilter,omitempty"`
	IncludePathFilter    string        `json:"includePathFilter,omitempty"`
	ExcludePathFilter    string        `json:"excludePathFilter,omitempty"`
}

type clientFilter struct {
	Types           []string `json:"types,omitempty"`
	ExcludeThinking *bool    `json:"excludeThinking,omitempty"`
	ExcludeProgress *bool    `json:"excludeProgress,omitempty"`
}

// serverMessage is a JSON message sent to a WebSocket client.
type serverMessage struct {
	ID             string                   `json:"id,omitempty"`
	Type           string                   `json:"type"`
	OK             *bool                    `json:"ok,omitempty"`
	Error          string                   `json:"error,omitempty"`
	Protocol       string                   `json:"protocol,omitempty"`
	ServerVersion  string                   `json:"serverVersion,omitempty"`
	UnknownType    string                   `json:"unknownType,omitempty"`
	Agents         []agentInfo              `json:"agents,omitempty"`
	TotalAgents    *int                     `json:"totalAgents,omitempty"`
	Conversations  []conv.ConversationInfo  `json:"conversations,omitempty"`
	SubscriptionID string                   `json:"subscriptionId,omitempty"`
	ConversationID string                   `json:"conversationId,omitempty"`
	Events         []conv.ConversationEvent `json:"events,omitempty"`
	EventCount     *int                     `json:"eventCount,omitempty"`
	Event          *conv.ConversationEvent  `json:"event,omitempty"`
	Cursor         string                   `json:"cursor,omitempty"`
	Progress       *snapshotProgress        `json:"progress,omitempty"`
	Agent          any                      `json:"agent,omitempty"`
	Name           string                   `json:"name,omitempty"`
	From           string                   `json:"from,omitempty"`
	To             string                   `json:"to,omitempty"`
	Reason                string                   `json:"reason,omitempty"`
	ConversationSupported *bool                    `json:"conversationSupported,omitempty"`
}

type snapshotProgress struct {
	Loaded int `json:"loaded"`
	Total  int `json:"total"`
}

type agentInfo struct {
	Name           string `json:"name"`
	Runtime        string `json:"runtime"`
	ConversationID string `json:"conversationId,omitempty"`
	WorkDir        string `json:"workDir"`
	Attached       bool   `json:"attached"`
}

func buildFilter(cf *clientFilter) conv.EventFilter {
	if cf == nil {
		return conv.EventFilter{}
	}
	filter := conv.EventFilter{}
	if len(cf.Types) > 0 {
		filter.Types = make(map[string]bool)
		for _, t := range cf.Types {
			filter.Types[t] = true
		}
	}
	if cf.ExcludeThinking != nil {
		filter.ExcludeThinking = *cf.ExcludeThinking
	}
	if cf.ExcludeProgress != nil {
		filter.ExcludeProgress = *cf.ExcludeProgress
	}
	return filter
}

// extractAgentFromConvID parses the conversation ID format "runtime:agentName:nativeId"
// to extract the agent name. Returns "" if the format is not recognized or the runtime
// prefix is unknown.
func extractAgentFromConvID(convID string) string {
	parts := strings.SplitN(convID, ":", 3)
	if len(parts) < 3 {
		return "" // not the standard 3-part format
	}
	if !slices.Contains(agents.RuntimePriority, parts[0]) {
		return "" // unknown runtime prefix
	}
	return parts[1]
}

func subID(n int) string {
	return "sub-" + strconv.Itoa(n)
}

func capSnapshot(events []conv.ConversationEvent) []conv.ConversationEvent {
	if len(events) > maxSnapshotEvents {
		return events[len(events)-maxSnapshotEvents:]
	}
	return events
}

func makeCursor(convID string, events []conv.ConversationEvent) string {
	if len(events) == 0 {
		return ""
	}
	last := events[len(events)-1]
	c := conv.Cursor{
		ConversationID: convID,
		Seq:            last.Seq,
		EventID:        last.EventID,
	}
	return encodeCursor(c)
}

func encodeCursor(c conv.Cursor) string {
	data, err := json.Marshal(c)
	if err != nil {
		log.Printf("wsconv: failed to marshal cursor: %v", err)
		return ""
	}
	return string(data)
}

func boolPtr(b bool) *bool {
	return &b
}
