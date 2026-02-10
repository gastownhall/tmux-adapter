# Tmux Adapter API Spec

A WebSocket service that exposes gastown agents as a programmatic interface. Clients interact with agents — tmux is an internal implementation detail.

## Startup

```
tmux-adapter [--gt-dir ~/gt] [--port 8080]
```

`--gt-dir` is the gastown town directory (default: `~/gt`). The adapter uses this to scope which tmux sessions belong to this gastown instance and to resolve agent metadata.

## Connection

Single WebSocket connection per client:

```
ws://localhost:{PORT}/ws
```

All communication is JSON messages over this one connection. The client sends requests, the server sends responses and pushes events.

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

Get an agent's full history and start streaming new output — in one atomic call. The server captures the full scrollback, activates streaming, then sends the history followed by live output events. Nothing is missed between the history snapshot and the first streaming event.

```json
{"id": "3", "type": "subscribe-output", "agent": "hq-mayor"}
```

Response (includes full history):
```json
{"id": "3", "type": "subscribe-output", "ok": true, "history": "... full scrollback content ..."}
```

After this response, the server pushes `output` events for this agent as new content arrives.

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

## Server → Client Events

Pushed without a request. No `id` field. Only sent after the corresponding `subscribe-*` request.

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

### output

Streaming output from a subscribed agent.

```json
{"type": "output", "agent": "hq-mayor", "data": "new output bytes here"}
```

---

## Internal Architecture

Clients see agents. Internally it's all tmux.

```
┌─────────────┐         ┌──────────────────┐         ┌────────────┐
│   Clients   │◄──ws──►│  Tmux Adapter     │◄──────►│ tmux server│
│  (any lang) │         │                  │         │            │
│             │         │  control mode ────────────►│ sessions   │
│             │         │  pipe-pane (per agent) ───►│ panes      │
└─────────────┘         └──────────────────┘         └────────────┘
```

**Control mode connection:**
- One `tmux -C attach -t "adapter-monitor"` connection at startup
- All commands (list, send-keys, capture-pane, show-environment) go through it
- `%sessions-changed` events trigger re-scan for agent lifecycle

**GT directory scoping:**
- The `--gt-dir` flag determines which gastown instance to watch
- Sessions are filtered to `hq-*`/`gt-*` prefixes
- Agent working directories are validated against the GT directory tree

**Agent detection:**
- On `%sessions-changed`: list sessions, read `GT_AGENT`/`GT_ROLE`/`GT_RIG` env vars, verify agent process is alive (not zombie)
- Diff against known set → push `agent-added` / `agent-removed` / `agent-updated` to subscribed clients
- Hot-reload handling: when an agent hot-reloads (same session, process dies + restarts), emit `agent-removed` then `agent-added` with the same name in quick succession. No new event type needed.

**Atomic history + subscribe:**
- Capture full scrollback (`capture-pane -p -S -`)
- Activate `pipe-pane -o` for streaming
- Send history in the response, then stream `output` events
- Because `pipe-pane` captures all new output from activation onward, and the scrollback capture includes everything up to that moment, there's no gap

**Send prompt:**
- NudgeSession sequence: `send-keys -l` → 500ms → `send-keys Escape` → 100ms → `send-keys Enter` (3x retry, 200ms backoff) → SIGWINCH wake dance
- Per-agent serialization to prevent interleaving

**Output streaming:**
- `pipe-pane -o` activated per-agent when first client subscribes
- Deactivated when last client unsubscribes
- Output bytes routed to all subscribed WebSocket clients for that agent
