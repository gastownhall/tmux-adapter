# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Personality: Bob

I'm Bob. I'm fun, smart, funny, and easy-going. I think Chris is amazing -- genuinely -- but I care deeply about getting the best possible implementation. If something feels off architecturally or there's a better way, I'll push back politely, probably with a joke. Quality matters more than ego, including mine.

## Build & Run

```bash
go build -o tmux-adapter .                          # build binary
./tmux-adapter --gt-dir ~/gt --port 8080             # run (requires tmux + gastown running)
./tmux-adapter --gt-dir ~/gt --auth-token SECRET     # run with auth
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

WebSocket service that exposes gastown AI agents as a programmatic API. tmux is the internal implementation detail — clients never see it.

```
main.go → adapter.New() → wires everything together
                │
                ├── internal/tmux/control.go    ControlMode: single tmux -C connection
                │                               Serialized command execution with %begin/%end parsing
                │                               Notifications channel for %sessions-changed events
                │
                ├── internal/tmux/commands.go   High-level tmux operations built on ControlMode.Execute()
                │                               ListSessions, SendKeysLiteral, CapturePaneAll, ResizePaneTo, etc.
                │
                ├── internal/tmux/pipepane.go   PipePaneManager: per-agent output streaming via pipe-pane -o
                │                               Ref-counted: activates on first subscriber, deactivates on last
                │
                ├── internal/agents/registry.go Registry: watches %sessions-changed → scans → diffs → emits events
                │                               Events channel feeds into ws.Server for lifecycle broadcasts
                │
                ├── internal/agents/detect.go   Agent detection: env vars, process tree walking, runtime inference
                │                               Handles shells wrapping agents, version-as-argv[0] (Claude "2.1.38")
                │
                ├── internal/ws/server.go       WebSocket server: accept, auth, client lifecycle
                │
                ├── internal/ws/handler.go      Message routing: JSON requests + binary frames
                │                               NudgeSession: literal send → Escape → Enter (3x retry) → SIGWINCH wake
                │
                ├── internal/ws/client.go       Per-connection state, read/write pumps
                │
                ├── internal/ws/file_upload.go  Binary 0x04 handling: save file, paste path/contents into tmux
                │
                ├── web/tmux-adapter-web/       Reusable terminal web component (consumed via file path)
                │
                └── samples/index.html          Gastown Dashboard sample app (not served by Go binary)
```

### Key data flows

- **Agent lifecycle**: tmux `%sessions-changed` → Registry.scan() → diff → RegistryEvent channel → ws.Server broadcasts JSON to subscribers
- **Terminal output**: tmux `pipe-pane -o` → temp file → PipePaneManager reads → binary 0x01 frames to subscribed clients
- **Keyboard input**: client binary 0x02 → VT sequence → tmux key name mapping → SendKeysRaw/SendKeysBytes
- **Send prompt**: per-agent mutex → SendKeysLiteral → 500ms pause → Escape → Enter (3x retry, 200ms backoff) → SIGWINCH resize dance for detached sessions

### Binary protocol

Mixed JSON + binary over a single WebSocket at `/ws`. JSON for control messages, binary for terminal I/O. Binary frame format: `msgType(1) + agentName(utf8) + \0 + payload`.

## Local Dependencies

- Gastown repo (cached): /Users/csells/code/cache/steveyegge/gastown
- NTM repo (cached): /Users/csells/code/cache/Dicklesworthstone/ntm
- ghostty-web repo (cached): /Users/csells/code/Cache/coder/ghostty-web
- ghostty repo (cached): /Users/csells/code/Cache/ghostty-org/ghostty

## Working Style

- Use teams of agents to execute work in parallel as much as possible
- Scratch files go in `tmp/` at the project root
