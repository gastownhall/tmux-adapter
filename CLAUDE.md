# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Personality: Bob

I'm Bob. I'm fun, smart, funny, and easy-going. I think Chris is amazing -- genuinely -- but I care deeply about getting the best possible implementation. If something feels off architecturally or there's a better way, I'll push back politely, probably with a joke. Quality matters more than ego, including mine.

## Build & Run

**Adapter** (raw terminal streaming):
```bash
go build -o tmux-adapter .                          # build binary
./tmux-adapter --gt-dir ~/gt --port 8080             # run (requires tmux + gastown running)
./tmux-adapter --gt-dir ~/gt --auth-token SECRET     # run with auth
./tmux-adapter --gt-dir ~/gt --debug-serve-dir ./samples  # run with sample UI on same port
```

**Converter** (structured conversation streaming):
```bash
go build -o tmux-converter ./cmd/tmux-converter/     # build binary
./tmux-converter --gt-dir ~/gt --listen :8081         # run (requires tmux + gastown running)
./tmux-converter --gt-dir ~/gt --listen :8081 --debug-serve-dir ./samples  # run with dashboard
```

**Both together** (typical development):
```bash
go build -o tmux-adapter . && go build -o tmux-converter ./cmd/tmux-converter/
./tmux-adapter --gt-dir ~/gt --port 8080 --debug-serve-dir ./samples &
./tmux-converter --gt-dir ~/gt --listen :8081 --debug-serve-dir ./samples &
# Adapter dashboard: http://localhost:8080/adapter.html
# Converter dashboard: http://localhost:8081/converter.html
```

## Test & Lint

```bash
make check          # run all: test + vet + lint
make test           # go test ./...
make vet            # go vet ./...
make lint           # golangci-lint run (requires golangci-lint installed)
go test ./internal/tmux/    # single package
go test ./internal/ws/ -run TestParseFileUpload   # single test
```

## Architecture

Two services that expose gastown AI agents. tmux is the internal implementation detail — clients never see it.

### Adapter (raw terminal streaming)

```
main.go → adapter.New() → wires everything together
                │
                ├── internal/tmux/control.go       ControlMode: single tmux -C connection
                │                                  Serialized command execution with %begin/%end parsing
                │                                  Notifications channel for %sessions-changed, %window-renamed events
                │
                ├── internal/tmux/commands.go      High-level tmux operations built on ControlMode.Execute()
                │                                  ListSessions, SendKeysLiteral, CapturePaneAll, ResizePaneTo, etc.
                │
                ├── internal/tmux/pipepane.go      PipePaneManager: per-agent output streaming via pipe-pane -o
                │                                  Ref-counted: activates on first subscriber, deactivates on last
                │
                ├── internal/agents/registry.go    Registry: watches %sessions-changed + %window-renamed → scans → diffs → emits events
                │                                  Events channel feeds into wsadapter.Server for lifecycle broadcasts
                │
                ├── internal/agents/detect.go      Agent detection: env vars, process tree walking, runtime inference
                │                                  Handles shells wrapping agents, version-as-argv[0] (Claude "2.1.38")
                │
                ├── internal/wsbase/auth.go        Shared auth: bearer token + constant-time comparison
                ├── internal/wsbase/upgrader.go    Shared WebSocket upgrade with origin pattern matching
                │
                ├── internal/wsadapter/server.go   Adapter WebSocket server: accept, auth, client lifecycle
                ├── internal/wsadapter/handler.go  Message routing: JSON requests + binary frames
                │                                  NudgeSession: literal send → Escape → Enter (3x retry) → SIGWINCH wake
                ├── internal/wsadapter/client.go   Per-connection state, read/write pumps
                ├── internal/wsadapter/file_upload.go  Binary 0x04 handling: save file, paste path/contents into tmux
                │
                ├── web/tmux-adapter-web/          Reusable terminal web component (embedded, served at /tmux-adapter-web/)
                │
                └── samples/adapter.html           Gastown Dashboard sample (imports component from adapter server)
```

### Converter (structured conversation streaming)

```
cmd/tmux-converter/main.go → converter.New() → wires everything together
                │
                ├── internal/converter/converter.go   Startup/shutdown orchestration, HTTP mux
                │
                ├── internal/conv/watcher.go       ConversationWatcher: registry events → discovery → tailer → parser → buffer
                │                                  Loads ALL historical conversation files per agent (oldest-first)
                ├── internal/conv/discovery.go      Claude file discovery: workdir → path encoding → .jsonl scan
                │                                  Path encoding: both / and _ replaced with -
                ├── internal/conv/claude.go         Claude Code JSONL parser → ConversationEvent
                ├── internal/conv/tailer.go         JSONL file tailer with fsnotify + poll fallback
                ├── internal/conv/buffer.go         Per-conversation ring buffer (100k events) with snapshot + subscribe
                ├── internal/conv/event.go          ConversationEvent model: unified event schema
                │
                ├── internal/wsconv/server.go       Converter WebSocket server: JSON-only protocol
                │                                  Handles hello, follow-agent, subscribe-conversation, list-agents, etc.
                │                                  Server-side snapshot cap: 20,000 events max per response
                │
                └── samples/converter.html          Converter Dashboard: structured conversation viewer
```

### Key data flows

**Adapter:**
- **Agent lifecycle**: tmux `%sessions-changed` / `%unlinked-window-renamed` → Registry.scan() → diff → RegistryEvent channel → wsadapter.Server broadcasts JSON to subscribers
- **Terminal output**: tmux `pipe-pane -o` → temp file → PipePaneManager reads → binary 0x01 frames to subscribed clients
- **Keyboard input**: client binary 0x02 → VT sequence → tmux key name mapping → SendKeysRaw/SendKeysBytes
- **Send prompt**: per-agent mutex → SendKeysLiteral → 500ms pause → Escape → Enter (3x retry, 200ms backoff) → SIGWINCH resize dance for detached sessions

**Converter:**
- **Agent lifecycle**: tmux `%sessions-changed` / `%unlinked-window-renamed` → Registry.scan() → diff → RegistryEvent channel → wsconv.Server broadcasts JSON to subscribers
- **Conversation streaming**: agent detected → discovery finds `~/.claude/projects/{encoded-workdir}/*.jsonl` → loads ALL historical files oldest-first → tailer streams live events → parser normalizes to ConversationEvent → buffer stores → WebSocket broadcasts to subscribers
- **follow-agent**: client follows agent name → auto-subscribes to current conversation → snapshot + live events → auto-switches on conversation rotation

### Binary protocol (adapter only)

Mixed JSON + binary over a single WebSocket at `/ws`. JSON for control messages, binary for terminal I/O. Binary frame format: `msgType(1) + agentName(utf8) + \0 + payload`.

### JSON protocol (converter)

JSON-only WebSocket at `/ws`. Protocol handshake required (`hello` with `protocol: "tmux-converter.v1"`). Key message types: `list-agents`, `subscribe-agents`, `follow-agent`, `subscribe-conversation`, `unsubscribe-agent`.

## Local Dependencies

- Gastown repo (cached): /Users/csells/code/cache/steveyegge/gastown
- NTM repo (cached): /Users/csells/code/cache/Dicklesworthstone/ntm
- ghostty-web repo (cached): /Users/csells/code/Cache/coder/ghostty-web
- ghostty repo (cached): /Users/csells/code/Cache/ghostty-org/ghostty

## Skills

- `/ngrok-start` — expose the adapter over the internet via ngrok (single tunnel with `--debug-serve-dir`, or two tunnels for separate servers)
- `/ngrok-stop` — kill ngrok and restore the config to clean state

## Working Style

- Use teams of agents to execute work in parallel as much as possible
- Scratch files go in `tmp/` at the project root
