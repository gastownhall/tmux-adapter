# Tmux Adapter API Spec

A WebSocket service that exposes gastown agents as a programmatic interface. Clients interact with agents — tmux is an internal implementation detail.

## Startup

```
tmux-adapter [--gt-dir ~/gt] [--port 8080] [--auth-token TOKEN] [--allowed-origins "localhost:*"] [--debug-serve-dir ./samples]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--gt-dir` | `~/gt` | Gastown town directory — scopes which tmux sessions belong to this instance |
| `--port` | `8080` | HTTP/WebSocket listen port |
| `--auth-token` | (none) | Require this token as `?token=` query param on WebSocket connections |
| `--allowed-origins` | `localhost:*` | Comma-separated origin patterns for CORS and WebSocket origin checks |
| `--debug-serve-dir` | (none) | Serve static files from this directory at `/` (development only) |

`--debug-serve-dir` is for development workflows where you want to serve a sample app on the same port as the adapter. This enables single-tunnel ngrok setups for mobile testing — one tunnel, one URL for both API and UI.

## Connection

Single WebSocket connection per client:

```
ws://localhost:{PORT}/ws
```

Communication uses JSON text frames plus binary frames over this one connection.

## Message Format

Every message has a `type` field. Requests from the client include an `id` for correlation. Responses echo the `id` back. Events are unsolicited (no `id`).

```json
// client → server (request)
{"id": "1", "type": "list-agents"}

// server → client (response to request)
{"id": "1", "type": "list-agents", "agents": [...]}

// server → client (unsolicited event)
{"type": "agent-added", "agent": {...}}
```

### Binary Frame Format

Terminal I/O frames use:

```
msgType(1 byte) + agentName(utf8) + 0x00 + payload(bytes)
```

| Type | Direction | Meaning |
|------|-----------|---------|
| `0x01` | server → client | terminal output bytes |
| `0x02` | client → server | keyboard input bytes |
| `0x03` | client → server | resize payload (`"cols:rows"`) |
| `0x04` | client → server | file upload payload (`fileName + 0x00 + mimeType + 0x00 + fileBytes`) |

Notes:
- Keyboard `0x02` payload is interpreted as VT bytes. Known special-key sequences (e.g. `ESC [ Z`) are translated to tmux key names (`BTab`, arrows, Home/End, PgUp/PgDn, F1-F12). Unknown sequences fall back to byte-exact `send-keys -H`.
- In the dashboard client, Shift+Tab is explicitly captured and sent as `ESC [ Z` to avoid browser focus traversal.
- File upload `0x04` payloads are capped at 8MB each, saved server-side, then pasted into tmux via tmux buffer operations. Text-like files up to 256KB paste inline; images (`image/*`) paste the absolute server-side path so agents can read and render them; other binary files paste a workdir-relative path (absolute fallback).

---

## Agent Model

An agent represents a live AI coding agent running in gastown. Only agents with an actual running process are exposed — zombie sessions (tmux alive, agent process dead) are filtered out.

```json
{
  "name": "hq-mayor",
  "role": "mayor",
  "runtime": "claude",
  "rig": null,
  "workDir": "/Users/me/gt/mayor/rig",
  "attached": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Agent identifier (e.g., `hq-mayor`, `gt-gastown-crew-max`) |
| `role` | string | Agent role: `mayor`, `deacon`, `overseer`, `witness`, `refinery`, `crew`, `polecat` |
| `runtime` | string | Agent runtime: `claude`, `gemini`, `codex`, `cursor`, `auggie`, `amp`, `opencode` |
| `rig` | string? | Rig name for rig-level agents, null for town-level agents |
| `workDir` | string | Working directory the agent is running in |
| `attached` | bool | Whether a human is currently viewing this agent's session |

---

## Client → Server Requests

### list-agents

Get the current set of all running agents.

```json
{"id": "1", "type": "list-agents"}
```

Response:
```json
{
  "id": "1",
  "type": "list-agents",
  "agents": [
    {"name": "hq-mayor", "role": "mayor", "runtime": "claude", "rig": null, "workDir": "/Users/me/gt/mayor/rig", "attached": true},
    {"name": "hq-deacon", "role": "deacon", "runtime": "claude", "rig": null, "workDir": "/Users/me/gt", "attached": false},
    {"name": "gt-gastown-crew-max", "role": "crew", "runtime": "gemini", "rig": "gastown", "workDir": "/Users/me/gt/gastown/crew/max/rig", "attached": false}
  ]
}
```

### send-prompt

Send a prompt to an agent. Enter is implied — the client just sends the text. The adapter handles the full send sequence internally (literal mode, debounce, Escape, Enter with retry, wake).

```json
{"id": "2", "type": "send-prompt", "agent": "hq-mayor", "prompt": "please review the PR"}
```

Response (after send completes):
```json
{"id": "2", "type": "send-prompt", "ok": true}
```

Error:
```json
{"id": "2", "type": "send-prompt", "ok": false, "error": "agent not found"}
```

### subscribe-output

Start output subscription (streaming by default).

```json
{"id": "3", "type": "subscribe-output", "agent": "hq-mayor"}
```

Response:
```json
{"id": "3", "type": "subscribe-output", "ok": true}
```

After this response, the server sends binary `0x01` frames:
1. Immediate snapshot frame (`capture-pane -p -e -S -`) for current pane state.
2. Ongoing live frames from `pipe-pane`.

To get history without subscribing, pass `"stream": false`:
```json
{"id": "4", "type": "subscribe-output", "agent": "hq-mayor", "stream": false}
```

This returns the history but does not activate streaming.

### unsubscribe-output

Stop streaming an agent's output.

```json
{"id": "5", "type": "unsubscribe-output", "agent": "hq-mayor"}
```

Response:
```json
{"id": "5", "type": "unsubscribe-output", "ok": true}
```

### subscribe-agents

Start receiving agent lifecycle events. The server immediately responds with the current agent list, then pushes `agent-added` / `agent-removed` events as agents come and go.

```json
{"id": "6", "type": "subscribe-agents"}
```

Response (includes current state):
```json
{
  "id": "6",
  "type": "subscribe-agents",
  "ok": true,
  "agents": [
    {"name": "hq-mayor", "role": "mayor", "runtime": "claude", "rig": null, "workDir": "/Users/me/gt/mayor/rig", "attached": true},
    {"name": "hq-deacon", "role": "deacon", "runtime": "claude", "rig": null, "workDir": "/Users/me/gt", "attached": false}
  ]
}
```

After this response, the server pushes `agent-added` / `agent-removed` events.

### unsubscribe-agents

Stop receiving agent lifecycle events.

```json
{"id": "7", "type": "unsubscribe-agents"}
```

Response:
```json
{"id": "7", "type": "unsubscribe-agents", "ok": true}
```

---

## Server → Client JSON Events

Pushed as JSON text frames without a request. No `id` field. Only sent after the corresponding `subscribe-*` request.

### agent-added

A new agent has become active — a real agent process is running, not just a tmux session appearing.

```json
{"type": "agent-added", "agent": {"name": "gt-gastown-crew-max", "role": "crew", "runtime": "gemini", "rig": "gastown", "workDir": "/Users/me/gt/gastown/crew/max/rig", "attached": false}}
```

### agent-removed

An agent has stopped or its session was destroyed.

```json
{"type": "agent-removed", "name": "gt-gastown-crew-max"}
```

### agent-updated

An agent's metadata has changed — typically when a human attaches to or detaches from the agent's session. Pushed to `subscribe-agents` subscribers.

```json
{"type": "agent-updated", "agent": {"name": "hq-mayor", "role": "mayor", "runtime": "claude", "rig": null, "workDir": "/Users/me/gt/mayor/rig", "attached": true}}
```

Terminal output is not sent as JSON. It is sent as binary `0x01` frames (see Binary Frame Format).

---

## HTTP Endpoints

In addition to the WebSocket at `/ws`, the adapter serves:

| Endpoint | Description |
|----------|-------------|
| `GET /tmux-adapter-web/*` | Embedded `<tmux-adapter-web>` web component files (CORS-enabled). The component is baked into the binary via `go:embed` — the adapter is its own CDN. |
| `GET /healthz` | Static process liveness check (`{"ok":true}`) |
| `GET /readyz` | tmux control mode readiness check (`200` on success, `503` with error) |
| `POST /debug/log` | Remote debug logging (only when `--debug-serve-dir` is set). Accepts plain text body, logs to server stderr as `[UI] ...`. Used for mobile debugging where browser DevTools aren't available. |
| `GET /*` | Static file serving from `--debug-serve-dir` (only when set). Development only. |

All HTTP responses include `Cache-Control: no-store` and `Access-Control-Allow-Origin: *` headers to prevent stale cached files on mobile browsers during development.

---

## Internal Architecture

Clients see agents. Internally it's all tmux.

```
┌─────────────┐         ┌──────────────────┐         ┌────────────┐
│   Clients   │◄──ws──►│  Tmux Adapter     │◄──────►│ tmux server│
│  (any lang) │         │                  │         │            │
│             │  http   │  control mode ────────────►│ sessions   │
│             │◄───────►│  pipe-pane (per agent) ───►│ panes      │
└─────────────┘         │  /tmux-adapter-web/ (embed)│            │
                        └──────────────────┘         └────────────┘
```

**Control mode connection:**
- One `tmux -C attach -t "adapter-monitor"` connection at startup
- All commands (list, send-keys, capture-pane, show-environment) go through it
- `%sessions-changed` and `%unlinked-window-renamed` events trigger re-scan for agent lifecycle

**GT directory scoping:**
- The `--gt-dir` flag determines which gastown instance to watch
- Sessions are filtered to `hq-*`/`gt-*` prefixes
- Agent working directories are validated against the GT directory tree

**Agent detection:**
- On `%sessions-changed` or `%unlinked-window-renamed`: list sessions, read `GT_AGENT`/`GT_ROLE`/`GT_RIG` env vars, verify agent process is alive (not zombie)
- Diff against known set → push `agent-added` / `agent-removed` / `agent-updated` to subscribed clients
- Hot-reload handling: when an agent hot-reloads (same session, process dies + restarts), emit `agent-removed` then `agent-added` with the same name in quick succession. No new event type needed.

**Atomic history + subscribe:**
- Activate `pipe-pane -o` for streaming
- Send JSON subscribe ack
- Send immediate binary snapshot (`capture-pane -p -e -S -`) so idle sessions render immediately
- Stream binary output frames from pipe-pane

**Send prompt:**
- NudgeSession sequence: `send-keys -l` → 500ms → `send-keys Escape` → 100ms → `send-keys Enter` (3x retry, 200ms backoff) → SIGWINCH wake dance
- Per-agent serialization to prevent interleaving

**Interactive keyboard path (`0x02`):**
- Client sends VT bytes from terminal `onData`
- Server maps known VT sequences to tmux key names (e.g. Shift+Tab, arrows, function keys)
- Remaining bytes are delivered exactly via `send-keys -H` (fallback to `-l` if `-H` unavailable)

**Output streaming:**
- `pipe-pane -o` activated per-agent when first client subscribes
- Deactivated when last client unsubscribes
- Output bytes routed to all subscribed WebSocket clients for that agent as binary `0x01` frames
