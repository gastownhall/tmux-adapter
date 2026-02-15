# PLAN: tmux-converter

## 1. Project Identity and Goals

**tmux-converter** is a new service in the tmux-adapter project that streams structured conversation data from CLI AI agents (Claude Code, Codex, Gemini) over WebSocket. Instead of piping raw terminal bytes that require client-side terminal emulation, tmux-converter watches the conversation files that these agents write to disk and streams normalized, structured JSON events to connected clients.

### Goals

1. **Structured over raw**: Replace terminal byte streaming with parsed, typed conversation events
2. **Universal rendering**: Enable desktop and mobile UIs to render agent conversations without terminal emulation (no ghostty-web dependency)
3. **Multi-runtime support**: Claude Code first, then Codex and Gemini — all normalized to one event schema
4. **Shared tmux layer**: Extract the tmux interaction code (control mode, commands, agent detection, registry) into a shared package used by both tmux-adapter and tmux-converter
5. **Zero-config discovery**: When an agent appears in tmux, automatically discover and start streaming its conversation — no manual file paths needed
6. **Real-time with history**: Clients connecting mid-conversation get a snapshot of all prior events, then seamless live streaming

### Non-Functional Targets

- **Latency**: End-to-end event latency p95 < 250ms on loopback
- **Recovery**: tmux disconnect to healthy streaming < 5s (automatic reconnect)
- **Correctness**: No silent data loss; all drops become explicit `stream-gap` or error responses
- **Resource**: Graceful degradation under load; bounded memory and goroutine growth

### Non-Goals

- tmux-converter does NOT replace tmux-adapter — both services coexist
- tmux-converter does NOT provide keyboard input to agents (that's tmux-adapter's job)
- tmux-converter does NOT render conversations — it provides data for clients to render however they choose
- tmux-converter does NOT manage agent lifecycle (start/stop agents) — it observes

---

## 2. Architecture Candidates

### Candidate A: Monolith Refactor

Refactor tmux-adapter to include both terminal streaming and conversation streaming as two modes of the same service. A single binary, single WebSocket endpoint, mode flag per subscription.

**Pros**: One binary to deploy, one port, shared connection pool. Clients can mix terminal and conversation subscriptions.

**Cons**: Increases complexity of the existing service. tmux-adapter is working and stable — bolting on a fundamentally different streaming model risks destabilizing it. The WebSocket handler becomes a rats' nest of mode checks. Testing surface doubles. Deployment coupling means you can't update conversation parsing without risking terminal streaming.

**Verdict**: Rejected. Violates single-responsibility. The two streaming models (raw bytes vs. structured JSON) are different enough to warrant separate services.

### Candidate B: Separate Binary, Shared Library

Two separate binaries (`tmux-adapter`, `tmux-converter`) that share a common Go library package for tmux interaction (`internal/tmux/`) and agent detection (`internal/agents/`). Each binary has its own main.go, WebSocket server, and streaming logic. Deployed independently, different ports.

**Pros**: Clean separation of concerns. Each service owns its streaming model. Shared library prevents code duplication for tmux interaction. Independent deployment and testing. Can run one or both.

**Cons**: Two processes connecting to tmux. Needs a shared ControlMode or two independent tmux -C connections (tmux supports multiple). Slightly more operational complexity.

**Verdict**: Selected. Clean architecture, natural code reuse, independent evolution.

### Candidate C: Plugin Architecture

A single binary with a plugin system. Core handles tmux interaction and WebSocket transport. Plugins provide streaming strategies (terminal-stream plugin, conversation-stream plugin). Plugin interface defines how to get data for an agent.

**Pros**: Extensible, single binary, elegant abstraction.

**Cons**: Over-engineered for two known use cases. Go's plugin system is fragile (build constraints, no Windows support). Interface boundaries add indirection without proportional benefit. YAGNI.

**Verdict**: Rejected. Premature abstraction. If a third streaming model emerges, we can refactor then.

### Selected Architecture: Candidate B (Separate Binary, Shared Library)

**Implementation: Two separate binaries.** The adapter is `tmux-adapter` (built from `main.go`), the converter is `tmux-converter` (built from `cmd/tmux-converter/main.go`). Both binaries share the same Go module and `internal/` packages.

Each binary has its own ControlMode connection and Registry instance (`adapter-monitor` and `converter-monitor` sessions respectively). This means two independent `tmux -C` sessions and duplicated `scan()` calls on every `%sessions-changed` / `%unlinked-window-renamed` event. This is intentional — it preserves full isolation between the adapter and converter, avoids shared-state coupling, and tmux handles multiple control clients efficiently.

The subcommand approach (single binary with `serve`/`converter`/`start` subcommands) was originally planned but deferred in favor of separate binaries for simplicity. A combined `start` command may be added later.

---

## 3. Technical Architecture

```
Two separate binaries, shared internal packages:

tmux-adapter (main.go)                     tmux-converter (cmd/tmux-converter/main.go)
┌─────────────────────────┐               ┌──────────────────────────────────┐
│   └── adapter.New()     │               │   └── converter.New()            │
│         │               │               │         │                        │
│  ┌──────┴──────┐        │               │  ┌──────┴──────┐                 │
│  │ wsadapter   │        │               │  │ wsconv      │ (JSON-only)     │
│  │ (JSON+bin)  │        │               │  │             │                 │
│  ├─────────────┤        │               │  ├─────────────┤                 │
│  │ PipePaneMgr │        │               │  │ ConvWatcher │ ← file watching │
│  │ (raw bytes) │        │               │  │ (parsed)    │                 │
│  └─────────────┘        │               │  ├─────────────┤                 │
│                         │               │  │ ConvBuffer  │ ← per-conv      │
│                         │               │  │ (ring buf)  │   event history  │
└────────┬────────────────┘               └──────┬─────────────────────────┘
         │                                       │
    ┌────┴───────────────────────────────────────┴────┐
    │              SHARED PACKAGES                     │
    │                                                  │
    │  internal/tmux/control.go    ControlMode         │
    │  internal/tmux/commands.go   tmux operations     │
    │  internal/agents/detect.go   Agent struct,       │
    │                              runtime detection   │
    │  internal/agents/registry.go scan/diff/events    │
    │  internal/wsbase/auth.go     shared auth logic   │
    │  internal/wsbase/upgrader.go shared WS upgrade   │
    └──────────────────────────────────────────────────┘
```

### Package Layout

```
.
├── main.go                               # adapter entrypoint
├── cmd/tmux-converter/main.go            # converter entrypoint (separate binary)
├── internal/
│   ├── tmux/                          # SHARED: tmux interaction
│   │   ├── control.go                 # ControlMode (parameterized session name)
│   │   ├── commands.go                # tmux operations
│   │   └── pipepane.go                # PipePaneManager (adapter only)
│   ├── agents/                        # SHARED: agent detection & registry
│   │   ├── detect.go                  # Agent struct, process detection
│   │   └── registry.go               # Registry scan/diff/events (accepts skip-list)
│   ├── adapter/                       # tmux-adapter wiring
│   │   └── adapter.go
│   ├── converter/                     # tmux-converter wiring
│   │   └── converter.go              # Startup/shutdown orchestration, HTTP mux
│   ├── conv/                          # Conversation watching & parsing
│   │   ├── watcher.go                 # ConversationWatcher: discovery + full history + tailing
│   │   ├── tailer.go                  # JSONL file tailer with fsnotify + poll fallback
│   │   ├── buffer.go                  # ConversationBuffer: ring buffer (100k events)
│   │   ├── discovery.go              # Claude file discovery (path encoding: / and _ → -)
│   │   ├── event.go                   # ConversationEvent: unified event model
│   │   └── claude.go                  # Claude Code JSONL parser
│   ├── wsbase/                        # SHARED: auth, origin checks
│   │   ├── auth.go                    # Bearer token + constant-time comparison
│   │   └── upgrader.go                # WebSocket upgrade with origin pattern matching
│   ├── wsadapter/                     # tmux-adapter protocol (JSON+binary)
│   │   ├── server.go                  # Adapter-specific server
│   │   ├── handler.go                 # Message routing
│   │   ├── client.go                  # Per-connection state, read/write pumps
│   │   └── file_upload.go             # Binary 0x04 handling
│   └── wsconv/                        # tmux-converter protocol (JSON-only)
│       └── server.go                  # Converter WS server (snapshot cap: 20k events)
├── web/tmux-adapter-web/              # embedded web component (adapter)
├── samples/
│   ├── adapter.html                   # Adapter dashboard
│   └── converter.html                 # Converter dashboard
└── Makefile
```

### External Dependencies

- `nhooyr.io/websocket`: WebSocket implementation (existing)
- `github.com/fsnotify/fsnotify`: Cross-platform file system notifications (NEW — required for conversation file watching)

### Binary Protocol

tmux-converter uses **JSON-only** WebSocket messages. No binary frames. This simplifies client implementation dramatically — any language with a JSON parser and WebSocket library can connect.

---

## 4. Subsystem Designs

### 4.1 Unified Conversation Event Model (`internal/conv/event.go`)

The core data structure that all parsers produce and all clients consume.

```go
// ConversationEvent is the universal event type streamed to clients.
// All three runtimes (Claude, Codex, Gemini) normalize into this.
type ConversationEvent struct {
    // Identity
    Seq            int64     `json:"seq"`            // monotonic within a conversation generation
    EventID        string    `json:"eventId"`        // stable identity (runtime uuid or generated), for dedupe
    GenerationID   string    `json:"generationId,omitempty"` // changes on file rotation/compaction; enables safe resume
    Type           string    `json:"type"`           // see EventType constants
    AgentName      string    `json:"agentName"`      // tmux session name
    ConversationID string    `json:"conversationId"` // session/file identifier
    Timestamp      time.Time `json:"timestamp"`

    // Content (varies by type)
    Role    string         `json:"role,omitempty"`    // "user", "assistant", "system"
    Content []ContentBlock `json:"content,omitempty"` // normalized content blocks
    Model   string         `json:"model,omitempty"`   // e.g. "claude-opus-4-6", "gpt-5.3-codex"

    // Metadata
    Runtime        string         `json:"runtime"`                 // "claude", "codex", "gemini"
    TokenUsage     *TokenUsage    `json:"tokenUsage,omitempty"`
    RequestID      string         `json:"requestId,omitempty"`     // for correlating streaming chunks
    ParentEventID  string         `json:"parentEventId,omitempty"` // for threading
    SubagentID     string         `json:"subagentId,omitempty"`    // if from a subagent
    ParentConvID   string         `json:"parentConvId,omitempty"`  // subagent's parent conversation
    DurationMs     int64          `json:"durationMs,omitempty"`    // for turn_duration events
    Metadata       map[string]any `json:"metadata,omitempty"`      // runtime-specific extra fields
}

// ContentBlock is a normalized content element.
type ContentBlock struct {
    Type      string          `json:"type"`                // "text", "thinking", "tool_use", "tool_result"
    Text      string          `json:"text,omitempty"`      // for text and thinking
    ToolName  string          `json:"toolName,omitempty"`  // for tool_use
    ToolID    string          `json:"toolId,omitempty"`    // for tool_use and tool_result
    Input     json.RawMessage `json:"input,omitempty"`     // for tool_use (preserved as raw JSON)
    Output    string          `json:"output,omitempty"`    // for tool_result
    IsError   bool            `json:"isError,omitempty"`   // for tool_result
    Signature string          `json:"signature,omitempty"` // for thinking blocks
    MimeType  string          `json:"mimeType,omitempty"`  // for image/file blocks (e.g., "image/png")
    Data      string          `json:"data,omitempty"`      // for image blocks: base64-encoded data
    Metadata  map[string]any  `json:"metadata,omitempty"`  // runtime-specific extra content properties
}

type TokenUsage struct {
    InputTokens  int `json:"inputTokens"`
    OutputTokens int `json:"outputTokens"`
    CacheRead    int `json:"cacheRead,omitempty"`
    CacheCreate  int `json:"cacheCreate,omitempty"`
}

// Event types
const (
    EventUser         = "user"
    EventAssistant    = "assistant"
    EventSystem       = "system"
    EventToolUse      = "tool_use"
    EventToolResult   = "tool_result"
    EventThinking     = "thinking"
    EventProgress     = "progress"
    EventTurnEnd      = "turn_end"
    EventQueueOp      = "queue_op"
    EventError        = "error"        // API errors, rate limits, permission denied
)
```

**Edge cases**:
- Claude assistant messages with `stop_reason: null` are streaming in progress — emit with type `assistant`, clients accumulate by `requestId`
- Claude `message.content` can be a string or array — parser normalizes to `[]ContentBlock`
- Claude API errors (rate limits, overloaded, permission denied) — emit as `EventError` with error details in `Content[0].Text` and error code in `Metadata["errorCode"]`
- Codex error events (`event_msg` with error payload) — emit as `EventError`
- Codex `compacted` events represent conversation summarization — emit as `system` with original content in metadata
- Gemini `thoughts` are separate from `content` — emit as `thinking` type ContentBlocks within the assistant event
- Empty/null content blocks are omitted, not sent as empty arrays

**Acceptance criteria**:
- All three parsers produce `ConversationEvent` values
- JSON serialization round-trips cleanly (marshal → unmarshal → deep equal)
- Every field has a clear owner — no "maybe this, maybe that" semantics
- Unit tests with real JSONL samples from each runtime

### 4.2 File Discovery (`internal/conv/discovery.go`)

Given an agent's working directory and detected runtime, find the conversation file(s) to watch.

```go
// Discoverer finds conversation files for a given agent.
type Discoverer interface {
    // FindConversations returns active files plus directories to watch when files are absent or rotating.
    FindConversations(workDir string) (DiscoveryResult, error)
}

type DiscoveryResult struct {
    Files     []ConversationFile
    WatchDirs []string    // fsnotify targets even when Files is empty (e.g., the projects dir)
}

type ConversationFile struct {
    Path                 string // absolute file path
    NativeConversationID string // runtime-native identifier (usually filename stem)
    ConversationID       string // globally unique protocol ID: "<runtime>:<agentName>:<nativeConversationID>"
    IsSubagent           bool   // true for agent-*.jsonl files
    Runtime              string // "claude", "codex", "gemini"
}
```

**ConversationID derivation rules** (must be stable across server restarts):
- **NativeConversationID**: basename without extension from the file path
  - Claude: `abc123.jsonl` → `"abc123"`
  - Codex: `rollout-550e8400-e29b-41d4-a716-446655440000.jsonl` → `"rollout-550e8400-e29b-41d4-a716-446655440000"`
  - Gemini: `session-42.json` → `"session-42"`
- **ConversationID** (protocol-level): `"<runtime>:<agentName>:<nativeConversationID>"` — e.g., `"claude:gt-rig1-witness:abc123"`
- IDs MUST be deterministic from (runtime, agentName, filePath) alone — no random generation. This enables clients to reconnect and resume by conversationId.
- Two agents with identical native filenames produce distinct ConversationIDs (different agentName component).

**Configurable roots** (CLI flags with env var fallback):
- `--claude-root` / `$CLAUDE_ROOT` (default: `~/.claude`)
- `--codex-root` / `$CODEX_ROOT` (default: `~/.codex`)
- `--gemini-root` / `$GEMINI_ROOT` (default: `~/.gemini`)

**Claude Code discovery algorithm**:
1. Encode workDir: replace all `/` with `-` AND all `_` with `-` (preserving leading `-` for the initial `/`). Claude Code's path encoding replaces both characters.
2. Scan `{claude-root}/projects/{encoded}/` for `*.jsonl` files
3. Sort by mtime descending — most recent is the active conversation
4. **Full history loading**: The watcher loads ALL conversation files per agent, not just the most recent. Older files are read oldest-first and their events are pre-loaded into the buffer before tailing begins. This gives clients the complete conversation history across session rotations.
5. Also scan for `agent-*.jsonl` files (subagents)
   - **Constraint**: V1 assumes subagent files reside in the same directory as the main conversation file.
6. For each subagent file, note `IsSubagent=true`
7. Return `WatchDirs` = `["{claude-root}/projects/{encoded}/"]` (or the matched directory) for fsnotify on conversation rotation

**Codex discovery algorithm**:
1. Compute today's date path: `{codex-root}/sessions/YYYY/MM/DD/`
2. Scan for `rollout-*.jsonl` files across configurable lookback window (default: 7 days via `--scan-lookback-days`)
3. **Validate**: For each candidate, read first line (`session_meta`), check if `payload.cwd` matches workDir
4. Most recent matching file is the active conversation
5. Also check yesterday's directory (sessions can span midnight)
6. Return `WatchDirs` = today's and yesterday's date-partitioned directories for fsnotify. This catches midnight rollover without requiring reconnect.

**Gemini discovery algorithm**:
1. Compute SHA-256 of workDir
2. Scan `{gemini-root}/tmp/{hash}/chats/` for `session-*.json` files
3. Sort by mtime descending — most recent is active
4. No subagent concept in Gemini

**Edge cases**:
- Agent workDir may differ from project root (e.g., agent in subdirectory) — try both exact match and parent directory
- Claude path encoding is lossy — collision resolution uses cwd/workDir validation from the first JSONL line (see algorithm step 5)
- New conversation file may not exist yet when agent first starts — register fsnotify on `DiscoveryResult.WatchDirs` and retry discovery on timer
- Stale files from previous sessions — use mtime filter to avoid attaching to old conversations
- **Validation**: Before attaching to any candidate file, inspect initial metadata (first event for JSONL, session info for JSON) to confirm cwd/project mapping. This prevents false-positive attachments when multiple agents share similar paths.

**Acceptance criteria**:
- Given a workDir and runtime, returns the correct file path (tested against real filesystem layout)
- Two agents with identical native filenames do not collide (distinct ConversationID values)
- Handles "file doesn't exist yet" by returning empty Files and populated WatchDirs for directory watching
- Returns subagent files for Claude Code
- Does not return stale files from days-old sessions
- Codex midnight rollover is discovered without reconnect (continuous follow across date partition boundary)

### 4.3 File Tailer (`internal/conv/tailer.go`)

Offset-tracked file reading that handles JSONL append-following and JSON whole-file replacement.

```go
// Tailer watches a conversation file and emits raw lines/content as it grows.
type Tailer struct {
    path     string
    runtime  string // determines tailing strategy
    offset   int64
    inode    uint64
    partial  []byte // incomplete line buffer (JSONL only)
    watcher  *fsnotify.Watcher
    events   chan []byte // complete lines/file contents
    ctx      context.Context
    cancel   context.CancelFunc
}

// NewTailer creates a tailer for the given file.
// If fromStart is true, reads from beginning (history replay).
// If fromStart is false, seeks to end (live-only).
func NewTailer(ctx context.Context, path string, runtime string, fromStart bool) (*Tailer, error)

// Lines returns a channel of complete raw data chunks.
// For JSONL runtimes (claude, codex): each chunk is one complete JSON line.
// For JSON runtimes (gemini): each chunk is the entire file content (on each change).
func (t *Tailer) Lines() <-chan []byte
```

**JSONL tailing algorithm** (Claude Code, Codex):
1. Open file, seek to position (0 for history, end for live)
2. Add file's directory to fsnotify watcher + start periodic poll fallback (1s interval, jittered ±200ms)
3. Event loop:
   a. On fsnotify Write event for our file OR poll tick:
      - Stat file: check inode (rotation) and size (truncation)
      - If inode changed: reset offset to 0, clear partial buffer, notify parser via Reset()
      - If size < offset: reset offset to 0, clear partial buffer, notify parser via Reset()
      - Read from offset to EOF
      - Split on `\n`, last incomplete segment goes to partial buffer
      - Emit each complete line to events channel
      - Update offset
   b. On fsnotify Create event in directory: check if our file appeared (for new conversations)
   c. Coalescing: drain burst events and cap read batch by time budget (100ms window)

The poll fallback ensures no data loss when fsnotify misses events (documented issue on some macOS/NFS setups).

**JSON tailing algorithm** (Gemini) — **debounced**:
1. Open file, read entire content
2. Add file to fsnotify watcher
3. **Debounce loop**:
   - On Write event: mark "dirty" and reset debounce timer (100ms)
   - If debounce timer fires (or max-wait of 500ms reached since first dirty mark):
     - Stat file (check size/modtime for change confirmation)
     - **Safety Check**: If file size > `MaxReReadFileSize` (default: 8MB), abort read and emit `EventSystem{Type: "warning", Text: "File too large for live tailing"}`. This prevents CPU exhaustion on massive logs.
     - Re-read entire file content
     - Emit entire content to the Lines() channel
   - **CPU work bound**: Under continuous writes at rate W/sec to a file of size F bytes, reads/sec is bounded by ceil(1/0.5) = 2 (max-wait dominates). Total read bandwidth = 2*F bytes/sec, versus W*F without debounce. For the motivating example (W=50, F=2MB): 4MB/s with debounce vs 100MB/s without — a 25x reduction. Combined with MaxReReadFileSize (8MB), absolute worst-case read bandwidth is 16MB/s per tailed file.
   - (Diffing/deduplication is the Parser's responsibility, not the Tailer's — separation of concerns)

**Edge cases**:
- File deleted while tailing (agent exited): emit EOF signal, stop tailer
- File created after tailer starts: watch directory, start reading when file appears
- Partial UTF-8 at buffer boundary: handled by buffering at line boundary, not byte boundary
- Very large lines (>1MB): set scanner buffer to 2MB, log warning if exceeded
- fsnotify event storm during heavy streaming: coalesce reads, not events

**Acceptance criteria**:
- JSONL tailer correctly handles partial lines across multiple reads (unit test with synthetic writes)
- JSONL tailer detects truncation and rotation (unit test with inode swap)
- Gemini parser correctly handles appends, rewrites, and compaction without missing or duplicating events
- No data loss: every complete line written to the file eventually appears on the events channel. **Liveness guarantee**: For every complete line L (terminated by `\n`) written to the file, if the tailer's context is not cancelled, L is emitted within at most 1.2 seconds (poll interval + jitter). This follows from the dual detection mechanism: fsnotify provides low-latency notification, and the 1s poll fallback provides guaranteed detection even when fsnotify misses events. **Condition**: Holds when the events channel consumer keeps up and lines are shorter than the 2MB scanner buffer.
- Graceful shutdown on context cancellation

### 4.4 Runtime Parsers (`internal/conv/claude.go`, `codex.go`, `gemini.go`)

Each parser implements the same interface, converting raw data into `ConversationEvent` values.

```go
// Parser converts raw conversation data into normalized events.
type Parser interface {
    // Parse converts a raw line (JSONL) or full content (JSON) into events.
    // May return multiple events per call (e.g., Gemini parsing all new messages).
    // For stateful parsers (Gemini), tracks what has already been processed internally.
    Parse(raw []byte) ([]ConversationEvent, error)

    // Reset clears any internal state (called when file is rotated/truncated).
    Reset()

    // Runtime returns the runtime name.
    Runtime() string
}
```

**Claude Code parser** (`claude.go`):
- Input: one JSONL line
- Parse top-level `type` field to determine event type
- Map `user` → `EventUser` with content normalization (string → single text block, array → map each block)
- Map `assistant` → `EventAssistant` with content block mapping:
  - `text` → `ContentBlock{Type: "text", Text: ...}`
  - `thinking` → `ContentBlock{Type: "thinking", Text: ..., Signature: ...}`
  - `tool_use` → `ContentBlock{Type: "tool_use", ToolName: ..., ToolID: ..., Input: ...}`
- Map `system` → `EventSystem` or `EventTurnEnd` (based on subtype)
- Map `progress` → `EventProgress`
- Map `queue-operation` → `EventQueueOp`
- Skip `file-history-snapshot` (not relevant for conversation rendering)
- Extract `TokenUsage` from `message.usage` on assistant events
- Set `RequestID` from assistant messages for streaming correlation
- Set `SubagentID` and `ParentConvID` for subagent files (detected by file path `agent-*`)

**Codex parser** (`codex.go`):
- Input: one JSONL line
- Parse `type` field: `session_meta`, `response_item`, `event_msg`, `turn_context`, `compacted`
- Map `event_msg` with `payload.type == "user_message"` → `EventUser`
- Map `response_item` with `payload.role == "assistant"` → `EventAssistant`
- Map `response_item` with `payload.role == "developer"` → `EventSystem`
- Map `turn_context` → `EventSystem` (metadata about the turn)
- Map `session_meta` → `EventSystem` (session start metadata)
- Map `compacted` → `EventSystem` (conversation was summarized)
- Extract model name from `turn_context.payload.model`

**Gemini parser** (`gemini.go`) — **stateful parser**:
- Input: full file content (JSON) — tailer emits entire file on each change (debounced)
- State: `lastSeenHashes map[uint64]struct{}` + `lastIndex int` + `lastDigest uint64` (file-level hash)
- On Parse(): parse full JSON array, compute stable message identity (hash of role + timestamp + content prefix). Compare with `lastSeenHashes` to detect appends, changes, and compaction. Emit only net-new messages. Update state.
- On Reset(): clear all state including `lastSeenHashes`
- **Memory bound**: If `len(lastSeenHashes)` exceeds 10,000, prune entries for messages no longer in the current file content. This prevents unbounded memory growth during long sessions where Gemini compacts/rewrites the file.
- **Lemma (Probabilistic correctness bound)**: With a 64-bit hash over (role, timestamp, content_prefix) and N messages, P(any collision) ≤ N²/2⁶⁵ (birthday bound). At N=10,000: P(collision) < 3×10⁻¹². The fallback re-emit mechanism provides a safety net: if a file-level digest change is detected, all messages are re-emitted and downstream deduplication by eventId handles correctness. **Implementation note**: the content_prefix should be at least 128 bytes to distinguish messages with identical role and timestamp.
- Fallback: if reconciliation fails (e.g., major rewrite detected via file-level digest change), re-emit all messages and let downstream dedupe by eventId
- Map `type == "user"` → `EventUser` with content as single text block
- Map `type == "gemini"` → `EventAssistant` with:
  - Main content as text block
  - `thoughts` as thinking blocks
  - `toolCalls` mapped to tool_use blocks (name, args, result)
- Extract model from `model` field
- Extract tokens from `tokens` field

**Edge cases**:
- Claude assistant messages with `stop_reason: null` — these are incremental streaming chunks. Emit them as-is; the client is responsible for accumulating (using `requestId` as the grouping key)
- Codex `response_item` with unknown `payload.type` — emit as `EventSystem` with raw payload preserved
- Gemini `toolCalls` with `status == "error"` — set `IsError: true` on the tool_result content block
- Malformed JSON lines — emit `EventError` with `Metadata["errorKind"]="parse"` and `Metadata["rawLineHash"]=...`; continue parsing subsequent input. Do not skip silently; let the UI know data is missing.
- Unknown event types from future runtime updates — emit as `EventSystem` with raw data in `Metadata["rawPayload"]`

**Acceptance criteria**:
- Each parser has unit tests using real JSONL/JSON samples extracted from actual conversation files
- Round-trip test: parse → serialize → verify all fields present
- Unknown event types are preserved, not dropped
- Malformed input is logged, surfaces exactly one `EventError` per bad line, and never causes a panic

### 4.5 Conversation Watcher (`internal/conv/watcher.go`)

Orchestrates discovery, tailing, and parsing for all active agents. This is the main coordination point.

```go
type ConversationWatcher struct {
    registry      *agents.Registry
    discoverers   map[string]Discoverer         // keyed by runtime name
    parsers       map[string]func() Parser      // parser factory, keyed by runtime (new instance per conversation)
    streams       map[string]*conversationStream // keyed by conversation ID
    activeByAgent map[string]string              // agent name → active conversation ID
    events        chan WatcherEvent              // output: lifecycle + conversation events
    mu            sync.RWMutex
}

type fileStream struct {
    path   string
    tailer *Tailer
    parser Parser   // isolated per file — prevents state cross-contamination between subagents
}

type conversationStream struct {
    conversationID string
    agent          agents.Agent
    files          map[string]*fileStream // one parser/tailer per file
    buffer         *ConversationBuffer
    cancel         context.CancelFunc
}

type WatcherEvent struct {
    Type      string              // "agent-added", "agent-removed", "agent-updated", "conversation-started", "conversation-switched", "conversation-event"
    Agent     *agents.Agent       // for lifecycle events
    Event     *ConversationEvent  // for conversation events
    OldConvID string              // for conversation-switched events
    NewConvID string              // for conversation-started and conversation-switched events
}
```

**Main loop**:
1. Start: initial registry scan, for each existing agent → start watching
2. Listen on `registry.Events()`:
   - On `added`: emit `agent-added` lifecycle event immediately, then call `startWatching(agent)`
   - On `removed`: call `stopWatching(agent.Name)`
   - On `updated`: emit lifecycle event (e.g., attached status change)

**Critical constraint**: The registry's events channel has a fixed buffer. The watcher's event loop MUST drain events quickly to prevent backpressure that blocks `registry.scan()`. Therefore `startWatching()` MUST be non-blocking: spawn goroutines for discovery/tailing, do not do synchronous I/O in the event loop.

3. `startWatching(agent)`:
   a. Look up discoverer for `agent.Runtime`; if no discoverer registered, emit `agent-added` lifecycle event with a log warning and return (see **Unknown runtime handling** below)
   b. Spawn a goroutine that calls `discoverer.FindConversations(agent.Name, agent.WorkDir)` (async — do not block the event loop)
   c. On discovery completion: separate non-subagent files from subagent files. For non-subagent files:
      - Most recent file (first by mtime) becomes the active conversation
      - ALL older files are read synchronously oldest-first via `loadHistoricalFile()` — each file is parsed line-by-line and events are tagged with the active conversation ID so they merge into one unified buffer
      - Historical events are pre-loaded into the buffer before the live tailer starts
   d. Create isolated `fileStream` for the active file (independent Tailer + Parser instance from factory)
   e. Also start fileStreams for any subagent files
   f. If no files found: register fsnotify on `DiscoveryResult.WatchDirs` and retry discovery on timer (default 5s via `--discovery-retry`)
4. **Directory watch for conversation rotation** (enables `follow-agent`):
   - For each active agent, maintain an `fsnotify.Watcher` on the conversation directory (e.g., `~/.claude/projects/{encoded}/`)
   - On `Create` event for a new `.jsonl` / `.json` file: re-run discovery for that agent
   - If discovery returns a newer file than the currently-tailed file (by mtime), trigger conversation rotation:
     a. Stop the old tailer(s)
     b. Start new tailer(s) for the new file
     c. Emit `WatcherEvent{Type: "conversation-switched", ...}` with old and new conversation IDs
     d. This event propagates to `follow-agent` subscribers as a `conversation-switched` message
5. `stopWatching(name)`:
   a. Cancel all tailers for this agent
   b. Emit "agent-removed" event
   c. Move the `conversationStream` from `streams` to a `graceStreams` map with an expiry timestamp (default: 5 minutes, configurable via `--buffer-grace-period`)
   d. A background goroutine periodically sweeps `graceStreams` every 30 seconds and removes expired entries, closing subscriber channels
   e. If the same agent restarts during the grace period, the old buffer in `graceStreams` is left to expire naturally — the new agent gets a fresh `conversationStream`

**Unknown runtime handling**:
When an agent is detected with a runtime that has no registered parser (e.g., `cursor`, `auggie`, `amp`, `opencode`):
1. Emit an `agent-added` lifecycle event so clients know the agent exists
2. Log a warning: `no conversation parser for runtime %q, agent %q — lifecycle events only`
3. Do NOT attempt file discovery or tailing — there's no parser to handle the output
4. The agent appears in `list-agents` and `subscribe-agents` responses with its detected runtime
5. Clients that try to `follow-agent` or `subscribe-conversation` for unsupported runtimes receive an error: `{"ok": false, "error": "runtime not supported for conversation streaming"}`

**Edge cases**:
- Agent starts before conversation file exists (agent is initializing) — retry discovery with backoff
- Multiple conversation files for same agent (previous + current sessions) — load ALL as history oldest-first, tail only the most recent for live events
- Agent restarts (removed then re-added quickly) — stop old tailers, start a fresh active stream; old stream remains only in grace retention until expiry
- Subagent file appears after main conversation is already being tailed — directory watcher detects new agent-*.jsonl, starts additional tailer

**Acceptance criteria**:
- Agent added → conversation streaming starts within 5 seconds
- Agent removed → tailers stopped, resources cleaned up, no goroutine leaks
- Late file appearance (agent starts before conversation file) → eventually discovers and streams
- Subagent files detected and correlated with parent conversation

### 4.6 Conversation Buffer (`internal/conv/buffer.go`)

Per-conversation event ring buffer that supports snapshot + live streaming. Keyed by conversationId (not agent name) to avoid cross-session contamination when agents rotate to new conversations. Default capacity: 100,000 events (sufficient for full conversation histories across multiple sessions).

```go
type ConversationBuffer struct {
    conversationID string
    agentName      string
    events    []ConversationEvent
    maxSize   int // max events to retain
    nextSeq   int64
    mu        sync.RWMutex
    subs      map[chan ConversationEvent]EventFilter
}

type EventFilter struct {
    Types           map[string]bool // nil = all types
    ExcludeThinking bool            // default false = thinking included
    ExcludeProgress bool            // default false = progress included
}

// Default filter behavior: A missing or empty filter means "send all events,
// including thinking and progress." Clients who want to suppress high-volume
// event types opt in to exclusion. This follows the principle of least surprise.
//
// Filter precedence:
// 1. Types is the authoritative allowlist when non-nil.
// 2. ExcludeThinking/ExcludeProgress apply only when Types is nil (all-types mode).
// 3. If Types is set, exclude flags cannot re-add excluded types.

// Append adds an event to the buffer and broadcasts to subscribers.
func (b *ConversationBuffer) Append(event ConversationEvent)

// Snapshot returns all buffered events (optionally filtered).
func (b *ConversationBuffer) Snapshot(filter EventFilter) []ConversationEvent

// Subscribe returns a channel that receives new events (filtered).
// Also returns the current snapshot atomically (no gap between snapshot and live).
func (b *ConversationBuffer) Subscribe(filter EventFilter) (snapshot []ConversationEvent, live <-chan ConversationEvent)

// Unsubscribe removes a subscriber.
func (b *ConversationBuffer) Unsubscribe(ch <-chan ConversationEvent)
```

**Snapshot + live handoff algorithm**:
1. Acquire write lock
2. Copy the events slice reference and length (NOT deep copy — events are immutable after Append)
3. Create subscriber channel, add to subs map
4. Release write lock
5. Return the snapshotted slice (safe because events are append-only and never mutated after creation) and channel

This ensures no events are missed between snapshot and live — the lock prevents any Append() during the handoff. The snapshot operation is O(1) (slice header copy) because the event ring buffer uses a copy-on-evict strategy: when the buffer is full, a new backing array is allocated and old events are not mutated. Individual events are treated as immutable after creation.

**Event size limits**: Individual `ContentBlock.Text` and `ContentBlock.Output` fields are capped at 256KB. Parser implementations MUST truncate oversized content and set `Metadata["truncated"] = true`. This bounds the memory footprint of the buffer and prevents a single large tool output from dominating memory.

**Theorem (Gap-Freedom)**: For every event e appended to the buffer, and for every subscriber that called Subscribe() either before or after Append(e), exactly one holds: (a) e appears in the snapshot, or (b) e is delivered to the live channel.

*Proof sketch*: Subscribe() and Append() both serialize on `sync.RWMutex`. Subscribe acquires the write lock, copies the events slice reference, registers the subscriber channel, then releases. Append acquires the write lock, appends the event, then iterates registered subscribers. Since both operations are within the same critical section, no event can fall between the snapshot copy and channel registration. ∀ events e, ∀ subscribers S: e ∈ snapshot(S) ∨ e ∈ live(S). **Implementation constraint**: Subscribe MUST use `Lock()` (not `RLock()`), and the snapshot copy and channel registration MUST happen within the same critical section.

**Broadcast algorithm** (in Append):
1. Assign `nextSeq` to event, increment
2. Append to events slice (evict oldest if at maxSize)
3. For each subscriber: if event passes filter, non-blocking send to bounded outbound channel (configurable depth, default 256)
4. If channel is full (slow consumer):
   a. Mark subscription as "paused" — no further events are enqueued
   b. Record the gap range (`fromSeq`/`toSeq`)
   c. Send the `stream-gap` notification through a **separate, dedicated control channel** (1-deep, per subscriber) that the write pump always checks first. This channel is never used for conversation events, so it cannot be blocked by the same backpressure.
   d. If the client does not `resume-conversation` within a configurable timeout (default 60s), close that subscription to reclaim resources.
   e. This ensures clients are always notified about gaps even when the data channel is full.

**Formal invariant (Notification Completeness)**: For every subscriber S, if one or more events are dropped due to channel fullness, a `stream-gap` notification is delivered via the control channel before any subsequent data events. This holds because: (1) the control channel is separate from and never contended by conversation events, (2) only one gap notification per pause cycle is needed (depth-1 suffices since the subscription is paused after the first gap), and (3) the write pump prioritizes the control channel. **Condition**: Relies on the WebSocket connection being alive (mitigated by ping/pong timeout detection).

**WebSocket keepalive**: Server sends ping every 15s, disconnects after 45s with no pong. This detects dead connections before buffers fill.

**Edge cases**:
- Buffer overflow: oldest events evicted, late-connecting clients get at most `maxSize` events
- Slow subscriber: non-blocking send prevents one slow client from blocking all broadcasts
- Concurrent Append + Subscribe: write lock ensures atomicity
- Agent removed while clients subscribed: emit protocol lifecycle message `conversation-ended` (not a `ConversationEvent`); subscription channels close on unsubscribe or grace expiry

**Acceptance criteria**:
- Subscribe returns snapshot + live channel with no gaps (concurrent test with rapid appends during subscribe)
- Events properly filtered per subscriber
- Filter precedence is deterministic (contract test matrix covers `Types` only, exclude flags only, and mixed inputs)
- Slow subscriber doesn't block fast ones
- Buffer size honored — oldest events evicted when full

### 4.7 WebSocket Protocol (`internal/wsconv/handler.go`)

JSON-only WebSocket protocol for tmux-converter.

**Protocol handshake (required first message)**:

```json
{"id": "req0", "type": "hello", "protocol": "tmux-converter.v1"}
```
→ Response: `{"id": "req0", "type": "hello", "ok": true, "protocol": "tmux-converter.v1", "serverVersion": "0.1.0"}`

If protocol version is unsupported, server responds with `{"ok": false, "error": "unsupported protocol version"}` and closes. This enables schema evolution without breaking existing clients.

**Client → Server messages**:

```json
{"id": "req1", "type": "list-agents"}
```
→ Response: `{"id": "req1", "type": "list-agents", "agents": [...]}`

```json
{"id": "req2", "type": "subscribe-agents"}
```
→ Response: `{"id": "req2", "type": "subscribe-agents", "ok": true, "agents": [...]}`
→ Subsequent events: `{"type": "agent-added", "agent": {...}}`, `{"type": "agent-removed", "name": "..."}`

```json
{"id": "req3", "type": "subscribe-conversation", "conversationId": "conv-123",
 "filter": {"types": ["user", "assistant"], "excludeThinking": true}}
```
→ Response: `{"id": "req3", "type": "conversation-snapshot", "subscriptionId": "sub-42", "conversationId": "conv-123", "events": [...], "cursor": "<opaque>"}`
→ Subsequent events: `{"type": "conversation-event", "subscriptionId": "sub-42", "conversationId": "conv-123", "event": {...}, "cursor": "<opaque>"}`

```json
{"id": "req4", "type": "follow-agent", "agent": "gt-rig1-witness",
 "filter": {"types": ["user", "assistant"], "excludeThinking": true}}
```
→ Response: `{"id": "req4", "type": "follow-agent", "ok": true, "subscriptionId": "sub-99", "conversationId": "conv-123", "events": [...], "cursor": "<opaque>"}`
→ Subsequent events: same as subscribe-conversation, plus lifecycle events:
→ `{"type": "conversation-switched", "subscriptionId": "sub-99", "agent": "gt-rig1-witness", "from": "conv-123", "to": "conv-124"}`
→ `{"type": "conversation-snapshot", "subscriptionId": "sub-99", "conversationId": "conv-124", "events": [...], "cursor": "<opaque>", "reason": "switch"}`
→ `{"type": "conversation-ended", "agent": "gt-rig1-witness", "conversationId": "conv-123"}`

`follow-agent` auto-subscribes to whichever conversation the agent is currently running and automatically switches when the agent rotates to a new conversation. `subscribe-conversation` locks to a specific conversation ID and never switches.

**Snapshot cap**: Snapshots returned by `follow-agent`, `subscribe-conversation`, and conversation switches are capped at 20,000 events (the most recent 20,000). This prevents oversized WebSocket messages when agents have very long histories. The `totalEvents` field in the response indicates the full buffer size so clients can display "showing last N of M" information.

**Ordering guarantee**: On switch, server emits `conversation-switched`, then `conversation-snapshot` for the new conversation, then live `conversation-event` for the new conversation. Clients see a clean cut: Stream A → Switch Marker → Snapshot B → Stream B.

**`follow-agent` transition logic**:
The server maintains strict ordering during a switch:
1. Watcher emits `conversation-switched` (or `conversation-started`).
2. WS handler identifies all clients following that agent.
3. For each client (serialized):
   a. Unsubscribe from old `ConversationBuffer` (if any).
   b. Send `conversation-switched` message to client.
   c. Subscribe to new `ConversationBuffer`.
   d. Send new buffer's snapshot to client.
   e. Begin streaming live events from new buffer.

**`follow-agent` resume logic**:
Clients MAY provide a `cursor` with `follow-agent`.
- If `cursor.conversationId` matches the agent's *current* conversation: standard resume (replay from cursor).
- If `cursor.conversationId` does NOT match (agent switched while client was disconnected): server ignores cursor, sends `conversation-switched` (if applicable) and full snapshot of the *current* conversation. The old conversation's missed events are not recoverable through this mechanism (the client treats the rotation as a clean break).

**Subscription multiplicity rules**:
- A client may have at most ONE `follow-agent` subscription per agent. Sending a second `follow-agent` for the same agent replaces the previous subscription (new filter applied, fresh snapshot returned).
- A client may have multiple `follow-agent` subscriptions for different agents simultaneously.
- A client may mix `follow-agent` and `subscribe-conversation` subscriptions.
- When `follow-agent` switches conversations, the old conversation subscription is automatically terminated; the client does not need to explicitly unsubscribe from the old conversationId.

```json
{"id": "req5", "type": "unsubscribe", "subscriptionId": "sub-42"}
```
→ Response: `{"id": "req5", "type": "unsubscribe", "ok": true}`

To unsubscribe from a `follow-agent`, the client sends `{"type": "unsubscribe-agent", "agent": "agent-name"}` (agent-level unsubscribe). The `unsubscribe` with `subscriptionId` detaches a specific `subscribe-conversation` binding.

```json
{"id": "req6", "type": "update-filter", "subscriptionId": "sub-42",
 "filter": {"types": ["user", "assistant", "tool_use"], "excludeThinking": false}}
```
→ Response: `{"id": "req6", "type": "update-filter", "ok": true}`

```json
{"id": "req7", "type": "resume-conversation", "subscriptionId": "sub-42", "cursor": "<opaque>"}
```
→ Response: `{"id": "req7", "type": "conversation-resume", "subscriptionId": "sub-42", "conversationId": "conv-123", "events": [...], "cursor": "<opaque>", "resumeMode": "exact"}`
→ If cursor expired/invalid: `{"id": "req7", "type": "stream-gap", "recoverable": false, "message": "Cursor expired; full resync required"}`
→ Then continues live streaming

**Resume cursor**: The server issues an opaque cursor encoding `{conversationId, generationId, seq, eventId}`. Clients never parse cursors — they just echo them back. The server MUST include a fresh cursor on every `conversation-event`, and clients SHOULD persist the latest cursor per `subscriptionId`. This decouples clients from internal sequencing and makes resume robust across buffer evictions and file rotations. **Note**: v1 cursors are in-memory only and are invalidated on server restart (see Section 7: Persistent cursor checkpoints). On restart, clients receive `stream-gap` with `recoverable: false` and must do a full resync.

**Formal guarantee (Resume Two-Outcome Completeness)**: A resume-conversation request produces exactly one of two outcomes: (1) **Exact resume** — the event at (generationId, seq) is in the buffer, and all events with seq > cursor.seq are returned with no gaps or duplicates; or (2) **Gap notification** — the event is not in the buffer (evicted or wrong generation), and `stream-gap` with `recoverable: false` is returned. There is no third outcome where events are silently missed. This follows from: generationId acts as an epoch identifier (invalidated on rotation/compaction), the ring buffer tracks its minimum retained seq, and eventId provides ABA-safety.

**Server → Client messages** (unsolicited):

```json
{"type": "agent-added", "agent": {"name": "gt-rig1-witness", "runtime": "claude", ...}}
{"type": "agent-removed", "name": "gt-rig1-witness"}
{"type": "agent-updated", "agent": {"name": "gt-rig1-witness", ...}}
{"type": "conversation-event", "subscriptionId": "sub-42", "conversationId": "conv-123", "event": {<ConversationEvent>}, "cursor": "<opaque>"}
{"type": "conversation-switched", "subscriptionId": "sub-99", "agent": "gt-rig1-witness", "from": "conv-123", "to": "conv-124"}
{"type": "conversation-snapshot", "subscriptionId": "sub-99", "conversationId": "conv-124", "events": [...], "cursor": "<opaque>", "reason": "switch"}
{"type": "conversation-ended", "conversationId": "conv-123", "agent": "gt-rig1-witness"}
{"type": "stream-gap", "subscriptionId": "sub-42", "conversationId": "conv-123", "fromSeq": 1042, "toSeq": 1099, "reason": "slow-consumer"}
```

**Edge cases**:
- Subscribe to agent that doesn't exist yet — return error, client can retry after receiving `agent-added`
- Subscribe to agent with no conversation file yet — return empty snapshot, stream events when file appears
- Client sends unknown message type — return error response with `unknownType` field
- Invalid JSON from client — close connection with WebSocket close code 1003 (unsupported data)

**Acceptance criteria**:
- All message types have request/response semantics (every request gets exactly one response)
- Snapshot + live streaming has no gaps (tested with concurrent event production)
- Filters correctly applied before sending
- Filter precedence is deterministic (contract test matrix covers `Types` only, exclude flags only, and mixed inputs)
- Resume correctly replays missed events from buffer
- On switch, `follow-agent` sends `conversation-switched` → `conversation-snapshot` for new conversation → live events (ordering guaranteed)
- Client receives `conversation-snapshot` for the new conversation immediately after `conversation-switched` and before live events
- Server includes updated opaque cursor on every `conversation-event`

### 4.8 Converter Wiring (`internal/converter/converter.go`)

Main initialization and shutdown orchestration.

```go
func New(opts Options) (*Converter, error)

type Options struct {
    GtDir               string        // gastown town directory
    ListenAddr          string        // e.g. "127.0.0.1:8081"
    AuthToken           string
    OriginPattern       string
    BufferSize          int           // max events per conversation buffer (default: 100000)
    StaleWindow         time.Duration // discovery staleness cutoff (default: 24h)
    BufferGracePeriod   time.Duration // removed-stream retention (default: 5m)
    ResumeTimeout       time.Duration // pause timeout after stream-gap (default: 60s)
    MaxReReadFileSize   int64         // Gemini full-read guard (default: 8MiB)
    DiscoveryRetry      time.Duration // retry interval when files absent (default: 5s)
    Debug               bool
}
```

**Initialization order**:
1. `tmux.NewControlMode(sessionName)` → connect to tmux. The `sessionName` is parameterized (`"converter-monitor"` for converter, `"adapter-monitor"` for adapter) so both can run independently. If the connection fails or drops, automatic reconnect with exponential backoff and jitter (default max 2s via `--tmux-reconnect-max-backoff`, hard cap 5s to preserve Recovery SLO).
2. `agents.NewRegistry(ctrl, gtDir)` → create agent registry
3. `conv.NewConversationWatcher(registry)` → create conversation watcher with discoverers and parsers
4. `wsconv.NewServer(watcher, listenAddr, authToken, originPattern)` → create WebSocket server
5. `registry.Start()` → initial scan + watch loop
6. `watcher.Start()` → begin watching conversations for known agents
7. On tmux reconnect: registry resync + watcher revalidation (re-discover files, restart tailers)
8. Forward watcher events to WebSocket broadcast
9. Set up HTTP mux:
   - `/ws` → WebSocket handler
   - `/healthz` → process alive + event loop responsive (checks goroutine health)
   - `/readyz` → tmux connected + registry synced + watcher healthy
   - `/conversations` → REST endpoint listing active conversations with metadata

**Shutdown order**:
1. HTTP server graceful shutdown (5s timeout)
2. Close all WebSocket clients
3. Watcher.Stop() → stop all tailers
4. Registry.Stop()
5. ControlMode.Close()

**CLI flags**:
```
--gt-dir DIR              Gastown town directory (required)
--listen ADDR             Listen address (default: 127.0.0.1:8081)
--auth-token TOKEN        Bearer auth token (required when listen is non-loopback)
--insecure-no-auth        Explicit opt-in for unauthenticated non-loopback binds
--origin PATTERN          Allowed WebSocket origins (default: loopback origins only)
--max-frame-bytes N       Max client message size (default: 1MiB)
--handshake-timeout DUR   WebSocket handshake timeout (default: 5s)
--claude-root DIR         Claude state root (default: ~/.claude)
--codex-root DIR          Codex state root (default: ~/.codex)
--gemini-root DIR         Gemini state root (default: ~/.gemini)
--scan-lookback-days N    Discovery lookback window for date-partitioned stores (default: 7)
--stale-window DUR        Max age of discovery candidates (default: 24h)
--buffer-grace-period DUR Retain removed conversation buffers for resume (default: 5m)
--resume-timeout DUR      Max pause after stream-gap before forced unsubscribe (default: 60s)
--max-reread-file-size N  Gemini full-file reread size cap in bytes (default: 8388608)
--discovery-retry DUR     Retry interval when no conversation file exists yet (default: 5s)
--tmux-reconnect-max-backoff DUR  Max reconnect backoff for tmux control channel (default: 2s, max: 5s)
--debug                   Enable debug logging
```

**Auth enforcement logic** (at startup, before listening):
1. Parse listen address to determine if it's loopback (`127.0.0.1`, `[::1]`, `localhost`)
2. If loopback: auth token is optional (safe for local development)
3. If non-loopback AND `--auth-token` is set: proceed normally
4. If non-loopback AND `--auth-token` is NOT set AND `--insecure-no-auth` is set: proceed with a loud warning on stderr
5. If non-loopback AND `--auth-token` is NOT set AND `--insecure-no-auth` is NOT set: **refuse to start** with error

**Acceptance criteria**:
- Binary builds and starts with `go build -o tmux-converter ./cmd/tmux-converter/ && ./tmux-converter --gt-dir ~/gt`
- Connects to tmux, discovers agents, starts streaming within 10 seconds of startup
- Graceful shutdown: no goroutine leaks, all connections closed
- Health/readiness endpoints respond correctly
- Forced tmux restart: converter recovers and resumes streaming automatically with p95 recovery < 5s

### 4.9 Shared tmux Package Refactoring

**What changes**: `main.go` becomes a subcommand dispatcher (serve/converter). The `internal/` packages remain where they are — they're already importable by both subcommands since they're in the same module.

**What changes in `internal/tmux/`**: `NewControlMode()` gains a `sessionName string` parameter (currently hardcoded to `"adapter-monitor"`). Existing adapter call site passes `"adapter-monitor"`, converter passes `"converter-monitor"`. This is the only signature change in the tmux package.

**What changes in `internal/agents/`**: `registry.scan()` currently skips only `"adapter-monitor"`. Change to accept a skip-list at construction (or skip any session matching `controlMode.SessionName()`). This prevents monitor sessions from either service appearing as phantom agents.

**What doesn't change**: `internal/tmux/` behavior (command execution, notifications, readLoop) and `internal/agents/` detection logic remain stable. `internal/adapter/` is unchanged.

**What changes**: existing `internal/ws/` is split into `wsbase/` (shared auth, upgrader, client primitives), `wsadapter/` (existing JSON+binary protocol), and `wsconv/` (new JSON-only protocol). This split prevents the adapter's binary frame handling from coupling with the converter's JSON-only model.

**Split details**:

`internal/wsbase/` (shared):
- `auth.go`: `isAuthorizedRequest()` — bearer token + constant-time comparison (extracted from `ws/server.go`)
- `upgrader.go`: WebSocket `Accept()` wrapper with origin pattern matching (extracted from `ws/server.go`)
- `client.go`: Base client struct with `conn`, `send chan`, read/write pump goroutines. Both adapter and converter embed this. The base client handles ping/pong, context cancellation, and graceful close. Message dispatch is delegated to a `MessageHandler` interface.

`internal/wsadapter/` (adapter-specific):
- `server.go`: Adapter server wrapping `wsbase` auth + upgrader. Owns `PipePaneManager` reference, binary frame dispatch.
- `handler.go`: Existing message routing logic (unchanged behavior, new package)
- `file_upload.go`: Binary 0x04 handling (unchanged)

`internal/wsconv/` (converter-specific):
- `server.go`: Converter server wrapping `wsbase` auth + upgrader. Owns `ConversationWatcher` reference, buffer subscriptions.
- `handler.go`: Conversation subscribe/filter/resume handlers

**Migration strategy**: Phase 1 creates `wsbase/` by extracting from `ws/` and updates `ws/` imports to use `wsbase/`. The existing `ws/` package is renamed to `wsadapter/`. This is a pure refactor with no behavioral changes, verified by existing tests passing. `wsconv/` is created empty in Phase 1 and populated in Phase 3.

**ControlMode sharing**: Both binaries (running as separate processes) each have their own ControlMode (separate tmux control sessions). tmux handles multiple control clients fine. The adapter uses `adapter-monitor`, the converter uses `converter-monitor`. Independent, no coordination needed.

**Build**:
```bash
go build -o tmux-adapter .
go build -o tmux-converter ./cmd/tmux-converter/
```

---

## 5. Implementation Phases

### Phase 1: Foundation (COMPLETE)

**Deliverables** (all done):
- Parameterized `tmux.NewControlMode(sessionName)` — adapter passes `"adapter-monitor"`, converter passes `"converter-monitor"`
- Updated `agents.NewRegistry()` to accept a session skip-list (prevents phantom agent detection of monitor sessions)
- Extracted `internal/wsbase/` from `internal/ws/` (auth, upgrader) and renamed `internal/ws/` to `internal/wsadapter/`
- Created `cmd/tmux-converter/main.go` as a separate binary entrypoint
- Created `internal/converter/converter.go` with full startup/shutdown orchestration
- All existing adapter tests pass with the `wsbase/` + `wsadapter/` split

### Phase 2: Core Conversation Infrastructure (COMPLETE)

**Deliverables** (all done):
- `internal/conv/event.go` — unified event model with JSON tags
- `internal/conv/tailer.go` — JSONL tailer with fsnotify + poll fallback
- `internal/conv/buffer.go` — ring buffer (100k events) with snapshot + subscribe
- `internal/conv/claude.go` — Claude Code JSONL parser
- Unit tests for all packages (`buffer_test.go`, `claude_test.go`, `tailer_test.go`, `watcher_test.go`, `discovery_test.go`)

### Phase 3: Discovery + Watcher + WebSocket (COMPLETE)

**Deliverables** (all done):
- `internal/conv/discovery.go` — Claude Code file discovery (path encoding: both `/` and `_` → `-`)
- `internal/conv/watcher.go` — orchestrator: registry → discovery → full history loading → tailer → parser → buffer
  - Loads ALL historical conversation files per agent oldest-first (not just most recent)
  - Cleans up replaced streams on re-discovery (prevents goroutine/FD leaks)
- `internal/wsconv/server.go` — WebSocket server with protocol handshake, event filtering, snapshot cap (20k events)
- `internal/converter/converter.go` — full wiring: ControlMode → Registry → Watcher → wsconv.Server → HTTP
- Protocol v1 handshake, `follow-agent`, `subscribe-conversation`, `subscribe-agents`, `list-agents`, `unsubscribe-agent`
- Server-side `excludeProgress` / `excludeThinking` filters
- `samples/converter.html` — dashboard with client-side filtering, render cap (2k DOM elements), server filter toggles
- HTTP endpoints: `/ws`, `/healthz`, `/readyz`, `/conversations`

### Phase 4: Multi-Runtime Support

**Deliverables**:
- `internal/conv/codex.go` — Codex JSONL parser
- `internal/conv/gemini.go` — Gemini JSON parser + whole-file diffing tailer strategy
- Discovery implementations for Codex and Gemini
- Updated watcher to register all three discoverers/parsers

**Acceptance criteria**:
- Codex agent conversations stream correctly
- Gemini agent conversations stream correctly (despite JSON-not-JSONL)
- Mixed agents (Claude + Codex running simultaneously) stream independently
- Parser unit tests with real samples from each runtime

### Phase 5: Polish and Production Readiness

**Deliverables**:
- Progress event throttling: server-side rate limiting of `EventProgress` events to at most 2/second per conversation (configurable). Latest event wins (drop intermediate, not queue).
- Subagent tree tracking for Claude Code
- `/conversations` REST endpoint
- Extended metrics (events/sec, buffer utilization, per-runtime parse latency)
- Updated CLAUDE.md with tmux-converter build/run instructions

**Acceptance criteria**:
- Progress events are throttled per conversation (test: emit 100 progress events in 1 second, verify client receives at most 2)
- Subagent events tagged with parent conversation ID
- REST endpoint returns accurate conversation metadata
- Soak test: multiple agents running simultaneously for extended period with no goroutine leaks or unbounded memory growth
- CLAUDE.md updated and accurate

---

## 6. Testing Strategy

### Unit Tests

- **Parsers**: Test each parser with real JSONL/JSON samples extracted from actual conversation files on this machine. Test malformed input, unknown event types, empty lines.
- **Parser fuzzing**: `go test -fuzz` on all runtime parsers (malformed JSON, truncated lines, huge payloads, binary garbage).
- **Tailer**: Test with synthetic files that simulate append, truncation, rotation. Use `os.Pipe()` or temp files with controlled writes. Test poll fallback by disabling fsnotify.
- **Buffer**: Concurrent subscribe + append test. Slow consumer test. Overflow test. Stream-gap test.
- **Buffer invariants**: Property tests for "snapshot + live has no gap" and monotonic cursor progression under concurrency.
- **Discovery**: Test against mock filesystem layouts. Test edge cases (no files, stale files, multiple matches). Test metadata validation rejects wrong-project files.
- **Protocol contract tests**: Golden request/response JSON fixtures for every message type and error code.
- **Race detection**: All packages run with `go test -race` in CI.

### Integration Tests

- **End-to-end**: Start tmux-converter against a real tmux server, create a session, write JSONL lines to a file, verify events arrive on WebSocket client.
- **Multi-agent**: Two agents with different runtimes, verify independent streaming.
- **Lifecycle**: Add agent → verify streaming starts. Remove agent → verify clean shutdown. Re-add → verify fresh start.
- **Fault injection**: fsnotify drop simulation, file rewrite during parse, tmux control disconnect/reconnect.
- **Reconnect**: Kill/restart tmux server, assert automatic recovery and bounded replay.
- **Soak**: Multi-agent run with leak assertions (goroutine count, fd count, heap growth bounds).

### Test Infrastructure

- Real JSONL samples committed to `testdata/` directory (anonymized). Collect samples in Phase 2 from actual agent runs on this machine.
- Test helper that creates temp dirs with Claude/Codex/Gemini file layouts
- WebSocket test client for integration tests

---

## 7. Exclusions (with rationale)

| Excluded | Rationale |
|----------|-----------|
| Keyboard input to agents | tmux-adapter handles this; tmux-converter is read-only |
| Terminal emulation | The entire point is to NOT need this |
| Agent start/stop control | Out of scope; tmux-converter observes, doesn't manage |
| Historical conversation search | Future feature; current focus is live streaming with recent history |
| Authentication federation | Use same bearer token approach as tmux-adapter |
| TLS termination | Use a reverse proxy (nginx, caddy) for TLS in production |
| Database storage | In-memory buffers sufficient; persistent storage is future work |
| Client-side SDK/library | Focus on WebSocket protocol; clients implement their own connection logic |
| Windows/Linux tmux differences | Mac-first (matches current tmux-adapter target) |
| Single-port subprocess/IPC mode | Deferred until protocol and operational behavior are stable in production |
| Privacy/redaction filtering | Future work; v1 trusts the auth boundary. Server-side regex redaction and `IncludeToolIO` filter are v2 candidates |
| Shared WatchHub (directory-level fsnotify fanout) | Good optimization for >10 concurrent agents; YAGNI for v1. Note as future scaling improvement |
| Persistent cursor checkpoints | Future work; v1 cursors are in-memory only, lost on restart |
| Client message rate limiting | v1 trusts the auth boundary; a valid auth token implies a trusted client. Per-client rate limiting is a v2 candidate if abuse is observed. Note: `--max-frame-bytes` provides payload size limiting. |
