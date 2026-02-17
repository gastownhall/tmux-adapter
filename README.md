# tmux-adapter

A WebSocket service that exposes [gastown](https://github.com/steveyegge/gastown) agents as a programmatic interface. Clients interact with agents — tmux is an internal implementation detail.

![screenshot](readme/screenshot.png)

## Quick Start

```bash
go build -o bin/tmux-adapter .
bin/tmux-adapter --port 8080
```

Connect with any WebSocket client:

```bash
websocat ws://localhost:8080/ws
```

### Sample Dashboard

The Gastown Dashboard lives in `samples/adapter.html` — a consumer of the WebSocket API, not part of the server. The adapter serves the `<tmux-adapter-web>` web component at `/tmux-adapter-web/`, so the sample (or any consumer) imports it directly from the adapter — no local file paths needed.

**Quick (single port, development):**

```bash
bin/tmux-adapter --port 8080 --debug-serve-dir ./samples
open http://localhost:8080
```

**Separate servers (production-like):**

```bash
# Terminal 1: start the adapter
bin/tmux-adapter --port 8080

# Terminal 2: serve the sample
python3 -m http.server 8000 --directory samples
open http://localhost:8000
```

When served separately, the sample connects to `localhost:8080` by default. To point at a different adapter (e.g. via ngrok), pass `?adapter=`:

```
http://localhost:8000/?adapter=abc123.ngrok-free.app
```

The `?adapter=` parameter controls both the WebSocket connection and the component import origin. If the adapter is behind TLS, the sample auto-upgrades to `wss://` and `https://`.

### Testing Over the Internet (ngrok)

The simplest approach uses `--debug-serve-dir` so only one ngrok tunnel is needed:

```bash
# 1. Start the adapter serving the sample
bin/tmux-adapter --port 8080 --debug-serve-dir ./samples

# 2. Expose via ngrok (single tunnel)
ngrok http 8080
```

For a stable URL across restarts, claim a free static domain at https://dashboard.ngrok.com/domains, then:

```bash
ngrok http --url your-name.ngrok-free.app 8080
```

To tear down:

```bash
pkill -f ngrok
```

## API

The adapter uses a mixed JSON + binary protocol over one WebSocket connection at `/ws`:
- JSON text frames for control flow (`subscribe-*`, `list-agents`, `send-prompt`)
- Binary frames for terminal data (output, keyboard input, resize)

Requests include an `id` for correlation; responses echo it back.

Security notes:
- WebSocket upgrades are checked against `--allowed-origins` (default: `localhost:*`). Cross-origin clients must be explicitly allowed.
- Optional auth token can be required via `--auth-token`; clients send `Authorization: Bearer <token>` or `?token=<token>`.

### Binary Frame Format

```
msgType(1 byte) + agentName(utf8) + 0x00 + payload(bytes)
```

| Type | Direction | Meaning |
|------|-----------|---------|
| `0x01` | server → client | terminal output bytes |
| `0x02` | client → server | keyboard input bytes |
| `0x03` | client → server | resize payload (`"cols:rows"`) |
| `0x04` | client → server | file upload payload (`fileName + 0x00 + mimeType + 0x00 + fileBytes`) |

### List Agents

```json
→ {"id":"1", "type":"list-agents"}
← {"id":"1", "type":"list-agents", "agents":[
    {"name":"my-project", "runtime":"claude", "workDir":"/Users/me/code/my-project", "attached":false},
    {"name":"research", "runtime":"gemini", "workDir":"/Users/me/code/research", "attached":false}
  ]}
```

### Send a Prompt

```json
→ {"id":"2", "type":"send-prompt", "agent":"hq-mayor", "prompt":"please review the PR"}
← {"id":"2", "type":"send-prompt", "ok":true}
```

The adapter handles the full NudgeSession delivery sequence internally (literal mode, 500ms debounce, Escape, Enter with retry, SIGWINCH wake for detached sessions).

### Upload + Paste Files

Clients can drag/drop or paste files into an agent terminal by sending binary `0x04` frames.

Behavior:
- Max upload size is 8MB per file.
- File bytes are transferred to the server and saved under `<agent workDir>/.tmux-adapter/uploads` (fallback: `/tmp/tmux-adapter/uploads/...`).
- If the file is text-like and <= 256KB, the file contents are pasted into tmux.
- Images (`image/*`) paste the absolute server-side path so that agents like Claude Code can read and render the image inline.
- Other binary files paste a relative server-side path (relative to the agent workdir when possible, absolute fallback).
- The adapter also attempts to mirror the same pasted payload into the server's local clipboard (`pbcopy`, `wl-copy`, `xclip`, `xsel`; best effort).

### Subscribe to Agent Output

Start streaming output (default `stream=true`):

```json
→ {"id":"3", "type":"subscribe-output", "agent":"hq-mayor"}
← {"id":"3", "type":"subscribe-output", "ok":true}
```

After this JSON ack, the server sends:
- a binary `0x01` snapshot frame with current pane content (so quiet/paused sessions are not blank)
- then ongoing binary `0x01` live stream frames from `pipe-pane`

History-only (no stream):

```json
→ {"id":"4", "type":"subscribe-output", "agent":"hq-mayor", "stream":false}
← {"id":"4", "type":"subscribe-output", "ok":true, "history":"..."}
```

Unsubscribe:

```json
→ {"id":"5", "type":"unsubscribe-output", "agent":"hq-mayor"}
← {"id":"5", "type":"unsubscribe-output", "ok":true}
```

### Subscribe to Agent Lifecycle

```json
→ {"id":"6", "type":"subscribe-agents"}
← {"id":"6", "type":"subscribe-agents", "ok":true, "agents":[...]}
← {"type":"agent-added", "agent":{...}}
← {"type":"agent-removed", "name":"gt-myrig-SomeTask"}
← {"type":"agent-updated", "agent":{...}}
```

`agent-updated` fires when a human attaches to or detaches from a session. Hot-reloads (same session, process restarts) emit `agent-removed` then `agent-added` in quick succession.

Unsubscribe:

```json
→ {"id":"7", "type":"unsubscribe-agents"}
← {"id":"7", "type":"unsubscribe-agents", "ok":true}
```

## Agent Model

```json
{
  "name": "my-project",
  "runtime": "claude",
  "workDir": "/Users/me/code/my-project",
  "attached": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Agent identifier (tmux session name) |
| `runtime` | string | Detected agent runtime: `claude`, `gemini`, `codex`, `cursor`, `auggie`, `amp`, `opencode` |
| `workDir` | string | Agent's working directory |
| `attached` | bool | Whether a human is viewing the session |

Only agents with a live process are exposed — zombie sessions are filtered out.

## tmux-converter

A companion service that streams **structured conversation events** from CLI AI agents over WebSocket. Instead of raw terminal bytes, it watches the conversation files agents write to disk (`.jsonl` for Claude Code) and streams normalized JSON events. The API is the primary interface — any WebSocket client can connect. The optional `<tmux-converter-web>` web component and sample dashboard are conveniences for quick visualization.

See `specs/converter-api.md` for the complete API reference.

### Quick Start

```bash
go build -o bin/tmux-converter ./cmd/tmux-converter/
bin/tmux-converter --listen :8081
```

### Converter Dashboard

```bash
bin/tmux-converter --listen :8081 --debug-serve-dir ./samples
open http://localhost:8081/converter.html
```

### Running Both Services

```bash
go build -o bin/tmux-adapter . && go build -o bin/tmux-converter ./cmd/tmux-converter/
bin/tmux-adapter --port 8080 --debug-serve-dir ./samples &
bin/tmux-converter --listen :8081 --debug-serve-dir ./samples &
# Adapter dashboard: http://localhost:8080/adapter.html
# Converter dashboard: http://localhost:8081/converter.html
```

### Converter API

JSON-only WebSocket protocol at `/ws`. Requires a protocol handshake as the first message:

```json
→ {"id":"1", "type":"hello", "protocol":"tmux-converter.v1"}
← {"id":"1", "type":"hello", "ok":true, "protocol":"tmux-converter.v1", "serverVersion":"0.1.0"}
```

**Follow an agent** (auto-subscribes to current conversation, auto-switches on rotation):

```json
→ {"id":"2", "type":"follow-agent", "agent":"hq-mayor", "filter":{"excludeProgress":true}}
← {"id":"2", "type":"follow-agent", "ok":true, "subscriptionId":"sub-1",
   "conversationId":"claude:hq-mayor:abc123", "conversationSupported":true}
← {"type":"conversation-snapshot", "subscriptionId":"sub-1", "conversationId":"claude:hq-mayor:abc123"}
← {"type":"conversation-snapshot-chunk", "subscriptionId":"sub-1", "events":[...], "progress":{"loaded":500,"total":835}}
← {"type":"conversation-snapshot-end", "subscriptionId":"sub-1", "conversationId":"claude:hq-mayor:abc123"}
← {"type":"conversation-event", "subscriptionId":"sub-1", "event":{...}, "cursor":"..."}
```

**List agents:**

```json
→ {"id":"3", "type":"list-agents"}
← {"id":"3", "type":"list-agents", "agents":[...]}
```

**Subscribe to agent lifecycle:**

```json
→ {"id":"4", "type":"subscribe-agents"}
← {"id":"4", "type":"subscribe-agents", "ok":true, "agents":[...], "totalAgents":5}
← {"type":"agent-added", "agent":{...}}
← {"type":"agent-removed", "name":"..."}
```

**List conversations:**

```json
→ {"id":"5", "type":"list-conversations"}
← {"id":"5", "type":"list-conversations", "conversations":[
    {"conversationId":"claude:hq-mayor:abc123", "agentName":"hq-mayor", "runtime":"claude"}
  ]}
```

**Subscribe to a conversation:**

```json
→ {"id":"6", "type":"subscribe-conversation", "conversationId":"claude:hq-mayor:abc123",
   "filter":{"types":["user","assistant"]}}
← {"id":"6", "type":"conversation-snapshot", "subscriptionId":"sub-2", "conversationId":"claude:hq-mayor:abc123"}
← {"type":"conversation-snapshot-chunk", ...}
← {"type":"conversation-snapshot-end", ...}
← {"type":"conversation-event", "subscriptionId":"sub-2", "event":{...}, "cursor":"..."}
```

**Send a prompt:**

```json
→ {"id":"7", "type":"send-prompt", "agent":"hq-mayor", "prompt":"please review the PR"}
← {"id":"7", "type":"send-prompt", "ok":true}
```

**Unsubscribe:**

```json
→ {"id":"8", "type":"unsubscribe-agent", "agent":"hq-mayor"}
← {"id":"8", "type":"unsubscribe-agent", "ok":true}
```

**Event filtering**: All subscription requests (`follow-agent`, `subscribe-conversation`) accept an optional `filter` object with `types` (allowlist), `excludeThinking`, and `excludeProgress` fields. Agent listing requests support session and path regex filters (`includeSessionFilter`, `excludeSessionFilter`, `includePathFilter`, `excludePathFilter`).

### Converter HTTP Endpoints

- `GET /ws` → WebSocket endpoint
- `GET /healthz` → process liveness (`{"ok":true}`)
- `GET /readyz` → tmux + registry readiness
- `GET /conversations` → list active conversations with metadata
- `GET /tmux-converter-web/*` → embedded web component files (CORS-enabled)
- `GET /shared/*` → shared dashboard assets (CORS-enabled)

### Converter Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--work-dir` | (empty) | Optional working directory filter — only track agents under this path (empty = all) |
| `--listen` | `:8081` | HTTP/WebSocket listen address |
| `--debug-serve-dir` | `` | Serve static files at `/` (development only) |

### How It Works

1. Connects to tmux via control mode (`converter-monitor` session)
2. Agent registry scans for gastown agents, emits lifecycle events
3. For each agent with runtime `claude`, discovers conversation files at `~/.claude/projects/{encoded-workdir}/*.jsonl`
4. Streams only the **active conversation** (most recent file) per agent — older files are inactive conversations from previous sessions
5. Parses Claude Code JSONL into normalized `ConversationEvent` structs
6. Buffers up to 100,000 events per conversation in a ring buffer
7. WebSocket clients get a snapshot (capped at 20,000 events) plus live streaming

**Active vs inactive conversations**: Each agent may have many `.jsonl` files — one per CLI session. Only the most recent is the *active conversation* and is streamed live. Older files are *inactive conversations* with stable ConversationIDs (e.g., `claude:agent-name:uuid`). Future: inactive conversations can be loaded on demand as independent read-only threads.

---

## Architecture

```
Clients ◄──ws──► tmux-adapter ◄──control mode──► tmux server
                      │
                      ├──pipe-pane (per agent)──► output files
                      │
                      └──/tmux-adapter-web/ ──► embedded web component (go:embed)

Clients ◄──ws──► tmux-converter ◄──control mode──► tmux server
                      │
                      └──file watching──► ~/.claude/projects/*/*.jsonl
```

- **Component serving**: the `<tmux-adapter-web>` web component is embedded in the adapter binary via `go:embed` and served at `/tmux-adapter-web/` with CORS headers. Consumers import directly from the adapter — the server is its own CDN.
- **Control mode**: each service maintains its own `tmux -C` connection (adapter uses `adapter-monitor`, converter uses `converter-monitor`)
- **Agent detection**: checks `pane_current_command` against known runtimes, walks process descendants for shell-wrapped agents, handles version-as-argv[0] (e.g., Claude showing `2.1.38`)
- **Output streaming** (adapter): `pipe-pane -o` activated per-agent on first subscriber, deactivated on last unsubscribe; each subscribe also sends an immediate `capture-pane` snapshot frame
- **Conversation streaming** (converter): discovers `.jsonl` files, tails only the active (most recent) file for live events, parses into structured events, buffers and broadcasts to subscribers. Older files are inactive conversations available for future on-demand loading.
- **Send prompt**: full NudgeSession sequence with per-agent mutex to prevent interleaving

## Adapter Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--work-dir` | (empty) | Optional working directory filter — only track agents under this path (empty = all) |
| `--port` | `8080` | WebSocket server port |
| `--auth-token` | `` | Optional WebSocket auth token |
| `--allowed-origins` | `localhost:*` | Comma-separated origin patterns for WebSocket CORS |
| `--debug-serve-dir` | `` | Serve static files from this directory at `/` (development only) |

## Adapter HTTP Endpoints

- `GET /tmux-adapter-web/*` → embedded web component files (CORS-enabled)
- `GET /healthz` → static process liveness (`{"ok":true}`)
- `GET /readyz` → tmux control mode readiness check (`200` on success, `503` with error on failure)

## Development Checks

```bash
make check
```

Architecture standards and constraints are documented in `ARCHITECTURE.md`.
