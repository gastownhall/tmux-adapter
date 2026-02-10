# Tmux Adapter Scenarios

Practical scenarios the adapter needs to support, with the underlying tmux commands.

---

## 1. List All Gastown Sessions

Gastown sessions use two prefixes: `hq-` (town-level) and `gt-` (rig-level). Every agent gets its own session.

**Basic listing:**
```bash
tmux list-sessions -F '#{session_name}' | grep -E '^(hq|gt)-'
```

**With status details:**
```bash
tmux list-sessions -F '#{session_name} | attached=#{session_attached} | #{session_created}' \
  | grep -E '^(hq|gt)-'
```

**With running process per session:**
```bash
for s in $(tmux list-sessions -F '#{session_name}' | grep -E '^(hq|gt)-'); do
  echo "$s: $(tmux list-panes -t "$s" -F '#{pane_current_command}')"
done
```

This mirrors `IsAgentRunning` — a session running a shell (`bash`, `zsh`, etc.) is a zombie; one running `node`, `claude`, `gemini`, etc. has a live agent.

**Source:** `ListSessions` (`tmux.go:544`), prefix filtering (`tmux.go:1788-1790`)

---

## 2. Send Text to an Agent (NudgeSession Pattern)

The canonical way to send a message to any gastown agent. The target is the session name — `hq-mayor`, `hq-deacon`, `gt-gastown-crew-max`, etc.

**Full sequence:**
```bash
SESSION="hq-mayor"
MESSAGE="your message here"

# 1. Send text in literal mode (-l prevents key name interpretation)
tmux -u send-keys -t "$SESSION" -l "$MESSAGE"

# 2. Wait 500ms for paste to complete (required for Claude Code)
sleep 0.5

# 3. Send Escape (exits vim INSERT mode if active, harmless otherwise)
tmux -u send-keys -t "$SESSION" Escape

# 4. Wait 100ms for mode switch
sleep 0.1

# 5. Send Enter separately (more reliable than appending to text)
#    Retry up to 3x with 200ms backoff on failure
tmux -u send-keys -t "$SESSION" Enter

# 6. Wake detached sessions via SIGWINCH resize dance
#    Skip if session is attached (check: tmux display-message -t "$SESSION" -p '#{session_attached}')
tmux -u resize-pane -t "$SESSION" -y -1
sleep 0.05
tmux -u resize-pane -t "$SESSION" -y +1
```

**Why each step matters:**
- **`-l` flag** — without it, tmux interprets key names (e.g., "Enter" in your text becomes an actual keypress)
- **500ms debounce** — Claude Code needs time to process the pasted text before Enter arrives
- **Escape** — Claude Code users may have vi-style bindings; Escape ensures normal mode
- **Separate Enter** — appending Enter to the text is unreliable
- **3x Enter retry** — gastown retries with 200ms backoff if `send-keys Enter` fails
- **SIGWINCH wake** — Claude's TUI event loop sleeps in detached sessions until a terminal resize event occurs
- **Serialization** — gastown uses a per-session mutex to prevent interleaving when multiple senders target the same session

**Multi-pane sessions:** If a session has multiple panes, target the agent pane specifically (e.g., `tmux -u send-keys -t "%5" -l "..."` using the pane ID). Gastown's `FindAgentPane` enumerates panes and finds the one running the agent process. Most sessions are single-pane, so targeting by session name works.

**Source:** `NudgeSession` (`tmux.go:780-821`), wake mechanism (`tmux.go:752-768`)

---

## 3. Stream Output from an Agent

**`pipe-pane` — real streaming:**
```bash
# Stream all new pane output to a file
tmux -u pipe-pane -t "hq-mayor" "cat >> /tmp/mayor-output.log"
tail -f /tmp/mayor-output.log

# Stream directly to a processing command
tmux -u pipe-pane -t "hq-mayor" "my-processing-command"

# Output-only (excludes input sent to the pane)
tmux -u pipe-pane -o -t "hq-mayor" "cat >> /tmp/mayor-output.log"

# Stop streaming (no command argument = disable)
tmux -u pipe-pane -t "hq-mayor"
```

`pipe-pane` sends all new bytes written to the pane to the specified command's stdin. No polling, no diffing — true byte-level streaming.

**Snapshots (non-streaming):**
```bash
# Last 50 lines
tmux -u capture-pane -p -t "hq-mayor" -S -50

# Full scrollback
tmux -u capture-pane -p -t "hq-mayor" -S -
```

`-p` prints to stdout. `-S -N` starts N lines before current position. `-S -` means from the very beginning.

**Note:** Gastown itself uses the snapshot approach (`capture-pane` polling), not `pipe-pane`. The adapter can choose either.

---

## 4. Detect Session Creation and Destruction

Three approaches, from simplest to most capable:

**Polling `list-sessions`:**
```bash
while true; do
  tmux list-sessions -F '#{session_name}' | grep -E '^(hq|gt)-' | sort > /tmp/gt-sessions-new
  diff /tmp/gt-sessions-old /tmp/gt-sessions-new
  mv /tmp/gt-sessions-new /tmp/gt-sessions-old
  sleep 1
done
```

**tmux hooks (event-driven):**
```bash
# Fire when any session is created
tmux set-hook -g session-created 'run-shell "echo created: #{session_name} >> /tmp/gt-events.log"'

# Fire when any session closes
tmux set-hook -g session-closed 'run-shell "echo closed: #{session_name} >> /tmp/gt-events.log"'

# Watch the log
tail -f /tmp/gt-events.log
```
Filter for `^(hq|gt)-` names on the receiving end.

**Control mode (best for programmatic use):**
```bash
# Create a throwaway session as connection anchor
tmux new-session -d -s "adapter-monitor"

# Attach in control mode — opens structured text protocol over stdin/stdout
tmux -C attach -t "adapter-monitor"
```
Control mode connects to the tmux **server**, not a specific session. You get notifications for all sessions on the server, regardless of who created them. The monitor session is just an anchor — it doesn't interfere with gastown's sessions.

tmux emits event notifications:
- `%sessions-changed` — a session was created or destroyed (by anyone, including gastown)
- `%session-changed $ID name` — attached session switched
- `%output %PANE_ID DATA` — pane output (if subscribed)

When you see `%sessions-changed`, send `list-sessions` through stdin to diff against your known set. Commands and queries go through the same connection.

This is what GUI tmux clients (iTerm2, tmux integration layers) use. For a programmatic wrapper, control mode is the natural foundation — session lifecycle events, pane output, and command sending all through one bidirectional connection.

---

## 5. Control Mode as Unified Adapter Foundation

Control mode (`tmux -C`) is an independent client connecting to the tmux server. Gastown doesn't need to start tmux in control mode — you connect alongside it as an observer with full access. Gastown doesn't know or care you're there.

**Setup:**
```bash
tmux new-session -d -s "adapter-monitor"
tmux -C attach -t "adapter-monitor"
```

**Through one stdin/stdout connection you can do everything:**

| Capability | Command (sent via stdin) |
|------------|-------------------------|
| List gastown sessions | `list-sessions -F '#{session_name}'` |
| Send text to an agent | `send-keys -t "hq-mayor" -l "message"` |
| Send Enter | `send-keys -t "hq-mayor" Enter` |
| Capture output snapshot | `capture-pane -p -t "hq-mayor" -S -50` |
| Read agent type | `show-environment -t "hq-mayor" GT_AGENT` |
| Read agent role | `show-environment -t "hq-mayor" GT_ROLE` |
| Check if attached | `display-message -t "hq-mayor" -p '#{session_attached}'` |
| Wake detached pane | `resize-pane -t "hq-mayor" -y -1` |
| Detect session lifecycle | Automatic: `%sessions-changed` events arrive on stdout |

**What control mode doesn't cover:**
- `pipe-pane` for true byte-level output streaming — still needs to be set up per-session as a side channel
- The NudgeSession timing (500ms debounce, Escape, Enter retry) — the adapter must implement these delays between commands sent through the control mode connection

**Architecture:** The adapter opens one control mode connection at startup. All tmux interaction flows through it. `pipe-pane` is set up per-session when output streaming is needed. The adapter never shells out to `tmux` as a subprocess — everything goes through the protocol.

