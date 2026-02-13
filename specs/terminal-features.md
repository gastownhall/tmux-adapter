# Terminal Features

All of the following terminal-like features for interacting with agents have to
work equally well on desktop and mobile browsers.

## Terminal Rendering: ghostty-web (WASM)

### Architecture

The server is a **raw byte pipe** — no terminal emulation. Terminal emulation
happens entirely client-side using [ghostty-web](https://github.com/coder/ghostty-web),
a WASM-compiled Ghostty terminal core with xterm.js-compatible API.

```
pipe-pane → raw bytes → batched at ~30fps → binary WebSocket → client
                                                                  ↓
                                                          ghostty-web (WASM)
                                                          full terminal emulation
                                                          (colors, cursor, alt screen)
                                                                  ↓
                                                          native terminal rendering
                                                    (desktop ✓  mobile ✓  ~400KB WASM)
```

### Why ghostty-web

- ~400KB WASM blob, MIT license, built by Coder (code-server team)
- Drop-in xterm.js replacement: `import { init, Terminal } from 'ghostty-web'`
- Mobile keyboard support fixed (the issue xterm.js has had for 8+ years)
- Endorsed by Mitchell Hashimoto (Ghostty author)

### Binary WebSocket Protocol

Terminal I/O uses binary WebSocket frames. JSON text frames handle all control
messages (agent lifecycle, subscribe/unsubscribe).

**Server → Client (binary):** `0x01 + agentName + \0 + rawBytes`
- Terminal output (history on subscribe, then live pipe-pane stream)

**Client → Server (binary):** `0x02 + agentName + \0 + rawBytes`
- Keyboard input from ghostty-web `onData` callback

**Client → Server (binary):** `0x03 + agentName + \0 + "cols:rows"`
- Terminal resize from ghostty-web `onResize` callback

**Client → Server (binary):** `0x04 + agentName + \0 + fileName + \0 + mimeType + \0 + fileBytes`
- File drag/drop or clipboard-file paste into the active terminal
- Max file size: 8MB per upload
- Server stores the file, copies paste payload to local clipboard (best effort), then pastes into tmux
- Text-like files <= 256KB paste inline; larger/binary files paste a server-valid saved file path (workdir-relative when possible)

**subscribe-output response (JSON):**
```json
{"id": "3", "type": "subscribe-output", "ok": true}
```
History arrives as a binary 0x01 frame immediately after the JSON response.
Live output follows as subsequent binary 0x01 frames.

### Client Architecture

- One `Terminal` instance per agent, cached in a `Map<string, {terminal, wrapper}>`
- Agent switching shows/hides wrapper divs (no terminal destruction)
- Keyboard input flows directly through the terminal — no separate prompt bar
- `ResizeObserver` + `onResize` keeps tmux pane dimensions in sync

### Implemented UX Semantics

- Selecting an agent shows/focuses its terminal immediately, then starts `subscribe-output`.
- Subscribe output behavior:
  - JSON ack: `{"type":"subscribe-output","ok":true}`
  - Immediate binary snapshot frame (`0x01`) from `capture-pane`
  - Then live binary stream frames (`0x01`) from `pipe-pane`
- Scroll behavior:
  - If user is at bottom, incoming output follows the stream.
  - If user scrolls up, incoming output preserves viewport position (sticky scroll).
  - If user starts typing while scrolled up, client jumps to bottom so typed input is visible.
- Special keys:
  - Browser terminal `onData` sends VT bytes.
  - Server maps common VT sequences to tmux key names (`BTab`, arrows, Home/End, PgUp/PgDn, F1-F12, Escape, Backspace).
  - Remaining bytes are sent byte-exact via `send-keys -H`.
  - Frontend explicitly captures Shift+Tab and sends `ESC [ Z` to avoid browser focus traversal.

### Mobile Keyboard Input (Implemented)

Mobile keyboard input works through a `beforeinput` event interception pattern:

1. ghostty-web sets `contenteditable="true"` on its parent host div
2. On mobile, tapping focuses the contenteditable div, not ghostty-web's internal textarea
3. `<tmux-adapter-web>` intercepts `beforeinput` events targeting the host div
4. Intercepted events are translated to terminal bytes and dispatched as `terminal-input` events
5. VKB open/close is consumer-controlled via `focusTextarea()` / `blurTextarea()`

The Gastown Dashboard sample adds a keyboard toggle button (⌨) in the header that calls these methods with a 500ms focus guard window to prevent the resize-triggered blur from dismissing the VKB.

### Outstanding Mobile Issues

1. **No touch scrolling** — Terminal output cannot be scrolled by touch gesture
2. **Header scrolls with VKB** — Header should stay pinned when virtual keyboard appears
3. **Audio input positioning** — Voice recording UI appears in wrong position above keyboard

---

## Terminal Command

Need to be able to do `!` and switch to terminal mode to enter a command.

## Terminal Mode (ala faux-term)

Need to be able to do `!` and press Enter and switch to a mode that looks like a
terminal, but isn't, executing each line like a terminal command, showing the
output, letting me do another command, etc. until I type the "exit" command.

## File Attachments

Implemented via binary `0x04` upload frames from the browser terminal:
- Drag/drop onto terminal uploads files.
- Clipboard file paste uploads files.
- Files are saved server-side under `.tmux-adapter/uploads` in the agent workdir
  (fallback under `/tmp/tmux-adapter/uploads`).
- Payload pasted into tmux:
  - text-like files (<=256KB): file contents
  - otherwise: server-side saved file path (workdir-relative when possible)

## @file Mentions

Need to be able to resolve @file mentions against the actual file system in the
cwd that the agent is running in.

## /command Executions

Need to be able to get a set of commands from each agent and provide for normal
/command-style syntax completion.
