# Converter API Spec

A WebSocket service that streams structured conversation events from CLI AI agents. Instead of raw terminal bytes, it watches conversation files agents write to disk and streams normalized JSON events. The API is the primary interface — any WebSocket client can connect. The optional `<tmux-converter-web>` web component and sample dashboard are conveniences for quick visualization.

## Startup

```
tmux-converter [--work-dir PATH] [--listen :8081] [--debug-serve-dir ./samples]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--work-dir` | (empty) | Optional working directory filter — only track agents under this path (empty = all agents) |
| `--listen` | `:8081` | HTTP/WebSocket listen address |
| `--debug-serve-dir` | (none) | Serve static files from this directory at `/` (development only) |

## Connection

Single WebSocket connection per client:

```
ws://localhost:{PORT}/ws
```

Communication uses JSON text frames. Binary frames are accepted only for file uploads (0x04).

## Protocol Handshake

The first message from the client **must** be a `hello` with the protocol version. Any other message before handshake returns an error.

```json
→ {"id":"1", "type":"hello", "protocol":"tmux-converter.v1"}
← {"id":"1", "type":"hello", "ok":true, "protocol":"tmux-converter.v1", "serverVersion":"0.1.0"}
```

If the protocol version is unsupported:
```json
← {"id":"1", "type":"hello", "ok":false, "error":"unsupported protocol version"}
```

Sending `hello` after handshake is already complete returns an error:
```json
← {"id":"2", "type":"error", "error":"already handshaked"}
```

## Message Format

Every message has a `type` field. Requests from the client include an `id` for correlation. Responses echo the `id` back. Unsolicited events have no `id`.

```json
// client → server (request)
{"id": "1", "type": "list-agents"}

// server → client (response to request)
{"id": "1", "type": "list-agents", "agents": [...]}

// server → client (unsolicited event)
{"type": "agent-added", "agent": {...}}
```

## Agent Model

An agent represents a live AI coding agent detected in a tmux session.

```json
{
  "name": "my-project",
  "runtime": "claude",
  "conversationId": "claude:my-project:abc123",
  "workDir": "/home/user/code/my-project",
  "attached": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Agent identifier (tmux session name) |
| `runtime` | string | Detected agent runtime: `claude`, `codex`, `gemini`, `cursor`, `auggie`, `amp`, `opencode` |
| `conversationId` | string | Active conversation ID (omitted if no active conversation) |
| `workDir` | string | Working directory the agent is running in |
| `attached` | bool | Whether a human is currently viewing this agent's tmux session |

---

## Client → Server Messages

### hello

Protocol handshake (required first message). See [Protocol Handshake](#protocol-handshake).

### list-agents

Get the current set of all running agents. Supports optional session and path regex filters.

```json
→ {"id":"1", "type":"list-agents"}
← {"id":"1", "type":"list-agents", "agents":[
    {"name":"my-project", "runtime":"claude", "conversationId":"claude:my-project:abc123", "workDir":"/home/user/code/my-project", "attached":true},
    {"name":"research", "runtime":"codex", "workDir":"/home/user/code/research", "attached":false}
  ]}
```

With filters:
```json
→ {"id":"2", "type":"list-agents", "includeSessionFilter":"^hq-", "excludePathFilter":"/tmp/"}
← {"id":"2", "type":"list-agents", "agents":[...]}
```

Filter errors return `ok: false`:
```json
← {"id":"2", "type":"list-agents", "ok":false, "error":"invalid regex in includeSessionFilter: ..."}
```

### subscribe-agents

Start receiving agent lifecycle events. The server immediately responds with the current agent list plus the unfiltered total count, then pushes `agent-added` / `agent-removed` / `agent-updated` events as agents come and go. Filters are stored on the connection and applied to all future lifecycle broadcasts.

```json
→ {"id":"3", "type":"subscribe-agents"}
← {"id":"3", "type":"subscribe-agents", "ok":true, "agents":[
    {"name":"my-project", "runtime":"claude", "workDir":"/home/user/code/my-project", "attached":true}
  ], "totalAgents":5}
```

With persistent filters:
```json
→ {"id":"3", "type":"subscribe-agents", "includePathFilter":"/home/user/code/"}
← {"id":"3", "type":"subscribe-agents", "ok":true, "agents":[...], "totalAgents":5}
```

The `totalAgents` field reflects the unfiltered count across the entire registry, useful for displaying "showing N of M agents" when filters are applied.

### list-conversations

List all active conversations with metadata.

```json
→ {"id":"4", "type":"list-conversations"}
← {"id":"4", "type":"list-conversations", "conversations":[
    {"conversationId":"claude:my-project:abc123", "agentName":"my-project", "runtime":"claude"},
    {"conversationId":"codex:research:rollout-550e", "agentName":"research", "runtime":"codex"}
  ]}
```

### subscribe-conversation

Subscribe to a specific conversation by ID. Returns a snapshot of buffered events followed by live streaming. The snapshot is capped at 20,000 events (most recent). Events are delivered in chunks.

```json
→ {"id":"5", "type":"subscribe-conversation", "conversationId":"claude:my-project:abc123",
   "filter":{"excludeThinking":true}}
← {"id":"5", "type":"conversation-snapshot", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123"}
← {"type":"conversation-snapshot-chunk", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123",
   "events":[...], "progress":{"loaded":500, "total":1200}}
← {"type":"conversation-snapshot-chunk", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123",
   "events":[...], "progress":{"loaded":1200, "total":1200}}
← {"type":"conversation-snapshot-end", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123"}
← {"type":"conversation-event", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123",
   "event":{...}, "cursor":"..."}
```

If the conversation is not yet available but the agent is known, the server creates a pending subscription that resolves when the conversation starts (30-second timeout).

Errors:
```json
← {"id":"5", "type":"error", "error":"conversationId required"}
← {"id":"5", "type":"error", "error":"conversation not found"}
← {"id":"5", "type":"error", "error":"conversation not found within timeout"}
```

### follow-agent

Auto-subscribe to an agent's active conversation. When the agent rotates to a new conversation (e.g., new Claude Code session), the subscription automatically switches — the client receives a `conversation-switched` event followed by a fresh snapshot.

```json
→ {"id":"6", "type":"follow-agent", "agent":"my-project", "filter":{"excludeProgress":true}}
← {"id":"6", "type":"follow-agent", "ok":true, "subscriptionId":"sub-2",
   "conversationId":"claude:my-project:abc123", "conversationSupported":true}
← {"type":"conversation-snapshot", "subscriptionId":"sub-2", "conversationId":"claude:my-project:abc123"}
← {"type":"conversation-snapshot-chunk", ...}
← {"type":"conversation-snapshot-end", ...}
← {"type":"conversation-event", ...}
```

If the agent has no active conversation yet, the subscription is registered as pending and resolves when a conversation starts:

```json
← {"id":"6", "type":"follow-agent", "ok":true, "subscriptionId":"sub-2", "conversationSupported":true}
```

The `conversationSupported` field indicates whether the agent's runtime supports conversation streaming. Runtimes without a registered discoverer (e.g., `cursor`, `auggie`, `amp`, `opencode`) return `conversationSupported: false` — the agent appears in lifecycle events but has no conversation data.

**Re-follow behavior**: Sending a second `follow-agent` for the same agent replaces the previous subscription (new filter applied, fresh snapshot). Multiple agents can be followed simultaneously.

### unsubscribe

Unsubscribe from a specific subscription by ID. Works for both `subscribe-conversation` and `follow-agent` subscriptions.

```json
→ {"id":"7", "type":"unsubscribe", "subscriptionId":"sub-1"}
← {"id":"7", "type":"unsubscribe", "ok":true}
```

### unsubscribe-agent

Unsubscribe from a `follow-agent` subscription by agent name. Also cleans up any pending conversation subscriptions for that agent.

```json
→ {"id":"8", "type":"unsubscribe-agent", "agent":"my-project"}
← {"id":"8", "type":"unsubscribe-agent", "ok":true}
```

### send-prompt

Send a text prompt to an agent. The converter handles the full NudgeSession delivery sequence internally (literal mode, debounce, Escape, Enter with retry, SIGWINCH wake for detached sessions). Per-agent serialization prevents interleaving.

```json
→ {"id":"9", "type":"send-prompt", "agent":"my-project", "prompt":"please review the PR"}
← {"id":"9", "type":"send-prompt", "ok":true}
```

Error:
```json
← {"id":"9", "type":"send-prompt", "ok":false, "error":"agent not found"}
```

---

## Server → Client Messages

### hello

Response to client handshake. See [Protocol Handshake](#protocol-handshake).

### list-agents

Response to `list-agents` request. Contains `agents` array and optionally `ok`/`error` on filter errors.

### subscribe-agents

Response to `subscribe-agents` request. Contains `agents` array, `ok`, and `totalAgents`.

### agent-added

A new agent has become active (pushed after `subscribe-agents`).

```json
{"type":"agent-added", "agent":{"name":"research", "runtime":"codex", "workDir":"/home/user/code/research", "attached":false}}
```

### agent-removed

An agent has stopped or its session was destroyed.

```json
{"type":"agent-removed", "name":"research"}
```

### agent-updated

An agent's metadata has changed (e.g., human attached/detached).

```json
{"type":"agent-updated", "agent":{"name":"my-project", "runtime":"claude", "workDir":"/home/user/code/my-project", "attached":true}}
```

### agents-count

Sent alongside `agent-added` and `agent-removed` events (not `agent-updated`). Provides the unfiltered total agent count so filtered dashboards can display "N of M agents".

```json
{"type":"agents-count", "totalAgents":5}
```

### list-conversations

Response to `list-conversations` request. Contains `conversations` array.

### follow-agent

Response to `follow-agent` request. Contains `ok`, `subscriptionId`, optionally `conversationId` (if an active conversation exists), and `conversationSupported`.

### conversation-snapshot

Start-of-snapshot marker. Sent when subscribing to a conversation or when a `follow-agent` subscription switches to a new conversation.

```json
{"type":"conversation-snapshot", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123"}
```

On conversation switch, includes `reason`:
```json
{"type":"conversation-snapshot", "subscriptionId":"sub-1", "conversationId":"claude:my-project:def456", "reason":"switch"}
```

### conversation-snapshot-chunk

Batched snapshot events. Chunks contain up to 500 events each with progress tracking.

```json
{
  "type": "conversation-snapshot-chunk",
  "subscriptionId": "sub-1",
  "conversationId": "claude:my-project:abc123",
  "events": [{...}, {...}, ...],
  "progress": {"loaded": 500, "total": 1200}
}
```

When history is still being read from disk, `total` is 0 (unknown):
```json
{"progress": {"loaded": 150, "total": 0}}
```

### conversation-snapshot-end

End-of-history marker. All events after this are live.

```json
{"type":"conversation-snapshot-end", "subscriptionId":"sub-1", "conversationId":"claude:my-project:abc123"}
```

### conversation-event

A live conversation event. Includes an opaque cursor for resume tracking.

```json
{
  "type": "conversation-event",
  "subscriptionId": "sub-1",
  "conversationId": "claude:my-project:abc123",
  "event": {<ConversationEvent>},
  "cursor": "{\"c\":\"claude:my-project:abc123\",\"s\":42,\"e\":\"evt-123\"}"
}
```

### conversation-switched

Sent to `follow-agent` subscribers when the agent rotates to a new conversation. After this message, the server sends a `conversation-snapshot` for the new conversation followed by live events.

```json
{
  "type": "conversation-switched",
  "subscriptionId": "sub-2",
  "agent": {<full agent object>},
  "from": "claude:my-project:abc123",
  "to": "claude:my-project:def456"
}
```

**Ordering guarantee**: `conversation-switched` → `conversation-snapshot` (with `reason: "switch"`) → `conversation-snapshot-chunk`(s) → `conversation-snapshot-end` → live `conversation-event`(s).

### send-prompt

Response to `send-prompt` request. Contains `ok` and optionally `error`.

### error

Error response to a client request.

```json
{"id":"5", "type":"error", "error":"conversationId required"}
```

For unknown message types, includes the offending type:
```json
{"id":"9", "type":"error", "error":"unknown message type", "unknownType":"foo"}
```

---

## ConversationEvent Model

The universal event type streamed to clients. All runtimes (Claude, Codex) normalize into this schema.

```json
{
  "seq": 42,
  "eventId": "evt-abc123",
  "generationId": "gen-1",
  "type": "assistant",
  "agentName": "my-project",
  "conversationId": "claude:my-project:abc123",
  "timestamp": "2026-02-16T10:30:00Z",

  "role": "assistant",
  "content": [
    {"type": "text", "text": "I'll help you with that."},
    {"type": "tool_use", "toolName": "Read", "toolId": "tool-1", "input": {"file_path": "/src/main.go"}}
  ],
  "model": "claude-opus-4-6",

  "runtime": "claude",
  "tokenUsage": {"inputTokens": 1200, "outputTokens": 350, "cacheRead": 800},
  "requestId": "req-xyz"
}
```

### Identity fields

| Field | Type | Description |
|-------|------|-------------|
| `seq` | int64 | Monotonic sequence number within a conversation |
| `eventId` | string | Stable event identity for deduplication |
| `generationId` | string | Changes on file rotation/compaction; enables safe resume |
| `type` | string | Event type (see below) |
| `agentName` | string | Agent identifier (tmux session name) |
| `conversationId` | string | Conversation identifier |
| `timestamp` | ISO 8601 | Event timestamp |

### Content fields

| Field | Type | Description |
|-------|------|-------------|
| `role` | string | `"user"`, `"assistant"`, or `"system"` |
| `content` | ContentBlock[] | Array of content blocks |
| `model` | string | Model name (e.g., `"claude-opus-4-6"`) |

### Metadata fields

| Field | Type | Description |
|-------|------|-------------|
| `runtime` | string | `"claude"`, `"codex"`, `"gemini"` |
| `tokenUsage` | object | Token consumption: `inputTokens`, `outputTokens`, `cacheRead`, `cacheCreate` |
| `requestId` | string | For correlating streaming chunks |
| `parentEventId` | string | For threading |
| `subagentId` | string | If event is from a subagent |
| `parentConvId` | string | Subagent's parent conversation |
| `durationMs` | int64 | For turn duration events |
| `metadata` | object | Runtime-specific extra fields |

### ContentBlock schema

```json
{
  "type": "tool_use",
  "toolName": "Read",
  "toolId": "tool-1",
  "input": {"file_path": "/src/main.go"},
  "output": "file contents...",
  "isError": false,
  "signature": "sig-abc",
  "mimeType": "image/png",
  "data": "<base64>",
  "metadata": {}
}
```

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `"text"`, `"thinking"`, `"tool_use"`, `"tool_result"`, `"image"` |
| `text` | string | For text and thinking blocks |
| `toolName` | string | For tool_use blocks |
| `toolId` | string | For tool_use and tool_result blocks |
| `input` | raw JSON | For tool_use blocks (preserved as raw JSON) |
| `output` | string | For tool_result blocks |
| `isError` | bool | For tool_result blocks |
| `signature` | string | For thinking blocks |
| `mimeType` | string | For image/file blocks |
| `data` | string | For image blocks (base64-encoded) |
| `metadata` | object | Runtime-specific extra content properties |

### Event types

| Type | Description |
|------|-------------|
| `user` | User message |
| `assistant` | Assistant response |
| `system` | System message |
| `tool_use` | Tool invocation |
| `tool_result` | Tool output |
| `thinking` | Model thinking/reasoning |
| `progress` | Progress update (high-volume) |
| `turn_end` | End of a conversation turn |
| `queue_op` | Queue operation |
| `error` | Error event |

---

## Event Filtering

Both `subscribe-conversation` and `follow-agent` accept an optional `filter` object to control which events are delivered.

```json
{
  "filter": {
    "types": ["user", "assistant", "tool_use", "tool_result"],
    "excludeThinking": true,
    "excludeProgress": true
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `types` | string[] | (all types) | Allowlist of event types. When set, only matching types are delivered. |
| `excludeThinking` | bool | `false` | Exclude `thinking` events. Only applies when `types` is not set. |
| `excludeProgress` | bool | `false` | Exclude `progress` events. Only applies when `types` is not set. |

**Filter precedence**:
1. `types` is the authoritative allowlist when non-nil.
2. `excludeThinking` / `excludeProgress` apply only when `types` is nil (all-types mode).

Filters apply to both snapshot events and live events.

---

## Session and Path Filtering

Agent listing and subscription requests accept optional regex filters to narrow results by session name or working directory path.

| Field | Type | Description |
|-------|------|-------------|
| `includeSessionFilter` | string | Regex — only include agents whose name matches |
| `excludeSessionFilter` | string | Regex — exclude agents whose name matches |
| `includePathFilter` | string | Regex — only include agents whose workDir matches |
| `excludePathFilter` | string | Regex — exclude agents whose workDir matches |

These filters are supported on `list-agents` (ephemeral, per-request) and `subscribe-agents` (persistent, stored on the connection and applied to all future lifecycle broadcasts).

Invalid regex patterns return an error response with `ok: false`.

---

## Snapshot Streaming Protocol

When a client subscribes to a conversation (via `subscribe-conversation` or `follow-agent`), the server delivers history as a chunked stream:

1. **`conversation-snapshot`** — start marker with subscription and conversation IDs
2. **`conversation-snapshot-chunk`** (repeated) — batches of up to 500 events each, with `progress` tracking:
   - `progress.loaded`: number of events sent so far
   - `progress.total`: total events when known (0 while history is still being read from disk)
3. **`conversation-snapshot-end`** — end-of-history marker

After `conversation-snapshot-end`, all subsequent messages for that subscription are live `conversation-event` messages with cursors.

Snapshots are capped at 20,000 events (most recent). The snapshot delivery uses a dedicated high-priority channel to prevent starvation by normal traffic.

---

## Binary File Upload

In addition to JSON text frames, the converter accepts binary WebSocket frames for file uploads using the same binary envelope format as the adapter.

**Frame format**: `0x04 + agentName(utf8) + 0x00 + fileName(utf8) + 0x00 + mimeType(utf8) + 0x00 + fileBytes`

- Maximum upload size: 8 MB per file
- Files are saved server-side then pasted into the agent's tmux session
- Text-like files up to 256 KB paste inline; images paste the server-side path
- Per-agent serialization prevents interleaving with prompts

---

## HTTP Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /ws` | WebSocket endpoint |
| `GET /healthz` | Process liveness check (`{"ok":true}`) |
| `GET /readyz` | tmux + registry readiness check (`{"ok":true}`) |
| `GET /conversations` | JSON list of active conversations with metadata |
| `GET /tmux-converter-web/*` | Embedded `<tmux-converter-web>` web component files (CORS-enabled) |
| `GET /shared/*` | Shared dashboard assets (CORS-enabled) |
| `GET /*` | Static file serving from `--debug-serve-dir` (only when set) |

---

## Web Component

The `<tmux-converter-web>` web component is served at `/tmux-converter-web/` as an embedded asset (via `go:embed`). It provides a ready-made conversation viewer that connects to the converter's WebSocket API. The component is a convenience — the WebSocket API documented above is the primary interface, and any client that speaks JSON over WebSocket can connect directly.
