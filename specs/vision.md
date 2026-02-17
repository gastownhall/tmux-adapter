# Tmux Adapter Vision

The tmux-adapter is a service that exposes gastown agents as a programmatic interface over WebSocket. Any client — a Go CLI, a Node.js server, a browser app — connects and interacts with agents without knowing anything about tmux.

## What It Does

- **Lists agents** — the current set of running agents with their metadata: what kind they are (Claude Code, Gemini, Codex, etc.), what folder they're in, and their name.
- **Streams agent output** — a client can ask for any agent's full history so far and optionally subscribe for streaming updates, in one atomic call so nothing is missed between the history snapshot and the first streaming event.
- **Sends prompts** — a client sends text to an agent and Enter is implied. The adapter handles all the delivery mechanics.
- **Tracks agent lifecycle** — clients subscribe to be notified when an agent becomes active or is deactivated. This means real agents, not zombie tmux sessions.
- **Scopes by working directory** — the client can optionally specify a working directory filter to watch only agents under a given path (default: all agents).

## What It Wraps

Gastown runs agents in tmux sessions. The adapter connects to the tmux server alongside gastown as an independent observer. It translates between the agent-centric API clients use and the tmux operations happening underneath:

- Listing agents = listing tmux sessions + filtering by prefix + checking that an agent process is actually alive
- Streaming output = tmux pipe-pane
- Sending prompts = the NudgeSession pattern (literal send, debounce, Escape, Enter with retry, wake)
- Tracking lifecycle = tmux control mode events
- Agent metadata = detected runtime process + pane working directory

Gastown supports 7 agent runtimes (Claude, Gemini, Codex, Cursor, Auggie, Amp, OpenCode) and the adapter handles all of them transparently.

## Who Uses It

Anyone who wants to programmatically interact with gastown agents:

- The gastown dashboard (Go) — adding streaming agent output
- A web frontend (browser) — monitoring agents in real time
- A Node.js service — exposing agents on a website
- CLI tools — scripting agent interactions
- Any language with a WebSocket client
