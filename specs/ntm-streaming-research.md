# Research: xterm.js, Control Mode, and the Dashboard Rendering Question

## Quick Answers

| Question | Answer |
|----------|--------|
| Would tmux control mode help? | We already use it. History = clean text. Live stream = raw bytes. There's a mismatch. |
| What would xterm.js give us? | A real terminal in the browser. Renders all ANSI/cursor gunk correctly. |
| Is it adaptive? | Yes — FitAddon + ResizeObserver auto-sizes to container. |
| Would it handle input too? | Yes — full bidirectional I/O, replaces the prompt text box entirely. |
| Mobile? | **No.** Long-standing open issues since 2017. Touch, virtual keyboard, copy/paste all broken. |

---

## 1. Does tmux control mode help with rendering?

**Short answer: partially — we already get the benefit, but only for history.**

Our tmux-adapter already uses control mode. Here's what the explorer found about our current output flow:

- **History** (`CapturePaneAll`): Uses `capture-pane -p -S -` **without `-e`**. Tmux interprets the terminal state and returns **clean plain text**. Colors, cursor movements, screen clears — all resolved into what-you-see-on-screen. This is already good.

- **Live stream** (`pipe-pane`): Uses `pipe-pane -o -t session 'cat >> /tmp/adapter-session.pipe'`. This gives **raw bytes** — every ANSI escape code, cursor movement, screen refresh flows through unchanged.

**The mismatch:** When a client subscribes, they get clean history followed by raw-bytes streaming. The deacon's output looks fine in the history snapshot but turns into gunk in the live stream.

**Could we fix this without xterm.js?** Yes, a few options:
- **Periodic capture-pane polling** instead of pipe-pane — clean text always, but adds latency (you'd poll every 100-500ms instead of true streaming)
- **Server-side ANSI stripping** — strip escape codes from the pipe-pane stream before sending to clients. Lossy (no colors) but readable
- **Server-side terminal emulation** — run a VT100 state machine on the server, emit rendered screen diffs. Complex but possible

---

## 2. What would xterm.js give us?

xterm.js is a **full VT100/xterm terminal emulator** that runs in the browser. It's what powers VS Code's integrated terminal, Hyper, Theia, and Replit.

**What it does:**
- Interprets all ANSI escape sequences (colors, bold, cursor movement, screen clearing, alternate screen buffer)
- Renders them into a canvas-based terminal display — looks and behaves like a real terminal
- The "inscrutable deacon gunk" would render **exactly** as it does in tmux — properly

**Architecture with our adapter:**
```
pipe-pane → raw bytes → WebSocket → xterm.js (browser)
                                      ↓
                                 renders correctly
                                 (cursor moves, colors, screen clears all work)
```

**Key addons:**
- **AttachAddon**: Connects xterm.js to a WebSocket. Bidirectional by default — keyboard input goes to server, server output goes to terminal
- **FitAddon**: Auto-sizes terminal to fit container element
- **WebGL addon**: GPU-accelerated rendering for performance
- **Search addon**: Ctrl+F search through scrollback

**What it replaces in our dashboard:**
- The `<pre>` output viewer → becomes a real terminal canvas
- The prompt text box → gone. You just type in the terminal
- The send button → gone. Enter key sends, just like a real terminal
- ANSI handling → fully solved, no server-side processing needed

---

## 3. Is it adaptive/responsive?

**Yes.** The FitAddon handles this well:

```js
const fitAddon = new FitAddon();
terminal.loadAddon(fitAddon);
terminal.open(container);
fitAddon.fit();  // size to container

// Auto-resize when container changes
new ResizeObserver(() => fitAddon.fit()).observe(container);
```

It calculates how many cols/rows fit in the container element based on font size and container dimensions. When the browser window resizes, the terminal resizes. You can also send the new cols/rows to the backend to resize the actual tmux pane.

---

## 4. Would it handle user input?

**Yes — this is one of the biggest wins.** The AttachAddon provides full bidirectional I/O:

```js
const socket = new WebSocket('ws://localhost:8080/ws');
const attachAddon = new AttachAddon(socket, { bidirectional: true });
terminal.loadAddon(attachAddon);
```

With this:
- User types → keystrokes go over WebSocket → server sends keys to tmux pane
- Pane output → pipe-pane → WebSocket → xterm.js renders it

This is a **real remote terminal** at that point, not a "dashboard with a prompt box." You'd interact with the agent exactly as if you were in tmux directly.

**Input features:**
- Full keyboard support (Ctrl+C, arrow keys, Tab completion, etc.)
- Custom key handlers (intercept specific keys)
- Copy/paste via Ctrl+Shift+C/V
- Mouse events (if the pane app uses mouse)
- Scrollback with mouse wheel

---

## 5. Would it work on mobile?

**This is the bad news.** Mobile support is xterm.js's Achilles heel.

Known issues (many open for years):
- [Issue #5377](https://github.com/xtermjs/xterm.js/issues/5377) (Jul 2025): "Limited touch support on mobile devices" — no dedicated touch event handling
- [Issue #1101](https://github.com/xtermjs/xterm.js/issues/1101) (2017): "Support mobile platforms" — **still open after 8+ years**
- [Issue #2403](https://github.com/xtermjs/xterm.js/issues/2403): Virtual/predictive keyboard doesn't work properly
- [Issue #3600](https://github.com/xtermjs/xterm.js/issues/3600): Erratic text output on Chrome Android
- [Issue #3727](https://github.com/xtermjs/xterm.js/issues/3727): Copy/paste broken on touch devices

**Bottom line:** xterm.js works great on desktop browsers (Chrome, Firefox, Safari, Edge). On mobile phones/tablets, it's effectively broken for input — you can see output but can't reliably type.

**Official browser support:** Latest versions of Chrome, Edge, Firefox, Safari on desktop. Mobile is not officially supported.

---

## Trade-off Summary

| Approach | Rendering | Input | Mobile | Complexity | Dependencies |
|----------|-----------|-------|--------|------------|--------------|
| **Current** (`<pre>` + prompt box) | Gunk in live stream | Prompt-only | Works fine | Minimal | Zero |
| **Current + ANSI strip** | Clean but no colors | Prompt-only | Works fine | Low | Zero |
| **Current + capture-pane polling** | Clean text | Prompt-only | Works fine | Low | Zero |
| **xterm.js** | Perfect rendering | Full terminal I/O | Broken | Medium | ~300KB JS bundle |
| **xterm.js + mobile fallback** | Perfect on desktop, clean text on mobile | Full on desktop, prompt box on mobile | Workable | High | ~300KB JS bundle |

---

## Recommendation

It depends on who's using this dashboard:

- **Desktop-only dev tool?** → xterm.js is the clear winner. You get a real terminal in the browser, the deacon output renders perfectly, and users can interact with agents naturally. It's what VS Code uses for a reason.

- **Needs to work on phones too?** → The current `<pre>` approach is actually more portable. We could add server-side ANSI stripping to make the live stream readable, or switch to periodic capture-pane polling for clean text. Not as fancy, but works everywhere.

- **Best of both worlds?** → xterm.js on desktop with a detection-based fallback to the current clean-text view on mobile. More code, but covers both use cases.

The 300KB bundle size is the other consideration — right now the dashboard is zero-dependency, which is kind of beautiful for a demo. Adding xterm.js means either a CDN link or bundling.
