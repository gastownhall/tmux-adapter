# tmux-adapter-web

A web component that hosts a ghostty-web terminal. Drop a
`<tmux-adapter-web>` tag into any page or framework, wire a few events, and
you get resize-aware, scroll-preserving, file-upload-capable terminal hosting
with zero framework dependencies.

---

## 1. Overview & Motivation

The terminal hosting logic that any client embedding ghostty-web terminals over
the tmux-adapter binary protocol needs has been extracted into a
`<tmux-adapter-web>` custom element:

- Custom fit logic with minimum 80-column enforcement via `scaleX` transform
- Flicker-free resize (hide during reflow, reveal on repaint or timeout)
- Drag-drop and clipboard-paste file upload with binary `0x04` framing
- Sticky scroll preservation (stay at bottom, or hold position while output streams)
- Initial-paint visibility gating (hidden until first snapshot renders, then auto-focus)
- Shift+Tab capture (prevent browser focus traversal, send CSI Z)
- Mobile touch device support: `beforeinput` interception for VKB keystrokes, consumer-controlled VKB via `focusTextarea()`/`blurTextarea()`

The Gastown Dashboard sample (`samples/index.html`) is a consumer of
this component — it handles agent lifecycle UI, WebSocket connection management,
sidebar, and mobile drawer, while the terminal hosting complexity lives entirely
inside `<tmux-adapter-web>`.

The adapter embeds the component files via `go:embed` and serves them at
`/tmux-adapter-web/`. Consumers import directly from the adapter — no local file
paths needed. The sample uses `?adapter=host:port` to control both the WebSocket
connection and the component import origin (default: `localhost:8080`). The Go
server accepts `localhost:*` origins by default; for cross-machine deployments,
use `--allowed-origins` on the server.

Each terminal is conceptually a UI component — one element per agent. The DOM
is the pool. Showing an agent means the element exists (or is visible); removing
an agent means removing the element. Frameworks already know how to manage this:
`v-if` in Vue, conditional rendering in React, `{#if}` in Svelte, or plain
`appendChild`/`removeChild` in vanilla JS.

---

## 2. Monorepo Structure

The repo restructures into two packages sharing a root:

```
tmux-adapter/
├── main.go                   # Go service entry point
├── internal/                 # Go service internals
│   ├── tmux/
│   └── ws/
├── go.mod
├── web/
│   ├── embed.go              # go:embed directive — bakes component into the binary
│   └── tmux-adapter-web/     # Reusable terminal web component (served at /tmux-adapter-web/)
│       ├── tmux-adapter-web.js  # Custom element definition (main entry)
│       ├── protocol.js       # Binary frame encode/decode (standalone export)
│       ├── fit.js            # Custom fit logic (min-cols + scaleX)
│       ├── file-transfer.js  # Drag-drop + paste upload wiring
│       └── index.js          # Barrel: auto-registers element, re-exports protocol
├── samples/
│   └── index.html            # Gastown Dashboard sample (imports component from adapter)
├── specs/
├── Makefile
└── README.md
```

### Why not a separate repo?

The web component and Go service evolve in lockstep — binary protocol changes
affect both sides simultaneously. A monorepo with path-based packages avoids
version coordination overhead while keeping concerns cleanly separated.

### Why not npm?

No build step, no bundler, no registry. The adapter embeds the component and
serves it — consumers import directly from the running server:

```js
const { decodeBinaryFrame, encodeBinaryFrame, BinaryMsgType } =
  await import('http://your-adapter:8080/tmux-adapter-web/index.js');
// <tmux-adapter-web> is now registered and ready to use
```

The component version is always in sync with the server's binary protocol —
no version coordination needed.

---

## 3. Public API

### `<tmux-adapter-web>` Custom Element

One element = one terminal. The element IS the terminal. It creates a
ghostty-web `Terminal` internally when connected to the DOM and tears it down
when disconnected.

#### Basic Usage

```html
<tmux-adapter-web
  name="hq-mayor"
  font-size="13"
  scrollback="10000"
  min-cols="80"
></tmux-adapter-web>
```

That's it. The element handles:
- Creating the ghostty-web `Terminal` and internal DOM structure
- `ResizeObserver` for auto-fitting when the element resizes
- Min-cols enforcement with `scaleX` on narrow viewports
- Flicker-free resize (hide during reflow, reveal on repaint)
- Initial-paint gating (hidden until first `write()` renders)
- Drag-drop and clipboard-paste file upload
- Shift+Tab capture (sends CSI Z instead of browser focus traversal)
- Sticky scroll preservation on incoming output
- Cursor hidden at (0,0) until app positions it via escape sequences

#### Attributes

All attributes are optional. Changes are reflected live via
`attributeChangedCallback`.

| Attribute | Type | Default | Description |
|-----------|------|---------|-------------|
| `name` | string | `""` | Identifier included in all event details. Typically the agent name. |
| `font-size` | number | `13` | Terminal font size in pixels. |
| `scrollback` | number | `10000` | Scrollback buffer size in lines. |
| `cursor-blink` | boolean | present | Cursor blinks when attribute is present. |
| `min-cols` | number | `80` | Minimum column count. Below this, `scaleX` shrinks to fit. |
| `theme-background` | string | `#0d1117` | Terminal background color. |

#### Methods

| Method | Signature | Description |
|--------|-----------|-------------|
| `write` | `write(data: Uint8Array): void` | Write terminal output bytes. Handles initial-paint reveal, resize-pending reveal, and sticky scroll preservation. |
| `reset` | `reset(): void` | Clear terminal, hide host, prepare for fresh snapshot. Next `write()` triggers reveal. |
| `focus` | `focus(): void` | Focus the terminal for keyboard input. On touch devices, skips auto-focus to prevent VKB popup. |
| `focusTextarea` | `focusTextarea(): void` | Focus the contenteditable host div to trigger the virtual keyboard on mobile. |
| `blurTextarea` | `blurTextarea(): void` | Blur the host div to dismiss the virtual keyboard on mobile. |

The API is intentionally tiny. The element manages its own lifecycle — creation
happens in `connectedCallback`, cleanup in `disconnectedCallback`. There's no
`destroy()` because removing the element from the DOM is the destroy.

#### Events

All events are `CustomEvent` instances dispatched on the element. The component
never touches transport — it just tells you what happened and lets you decide
how to send it.

| Event | `detail` | Fired when |
|-------|----------|------------|
| `terminal-input` | `{ name: string, data: Uint8Array }` | User types in the terminal (from ghostty-web `onData`). |
| `terminal-resize` | `{ name: string, cols: number, rows: number }` | Terminal dimensions changed after fit (debounced, 90ms). |
| `file-upload` | `{ name: string, payload: Uint8Array }` | File dropped or pasted. `payload` is pre-framed: `fileName + \0 + mimeType + \0 + fileBytes`. |
| `terminal-ready` | `{ name: string }` | Terminal is created and ready for `write()`. Fires once after `connectedCallback`. |

The consumer is responsible for:
1. Listening to these events and sending the appropriate binary frames over the wire
2. Receiving binary `0x01` frames from the server and calling `element.write(data)`

This clean separation means the component works with any transport — WebSocket,
WebRTC, postMessage, mock harness for testing.

#### Element Lifecycle

```
connectedCallback:
  1. Create internal host div (.tmux-adapter-web-host)
  2. Inject scoped styles (if not already present)
  3. Mark pending initial paint BEFORE terminal.open() (suppresses onResize during open)
  4. Create ghostty-web Terminal with current attribute values
  5. terminal.open(hostDiv)
  6. Hide cursor at (0,0) — write '\x1b[?25l'
  7. Hide host until first write reveals it
  8. Wire onData → dispatch 'terminal-input' (scrolls to bottom on user input)
  9. Wire onResize → debounce → dispatch 'terminal-resize' (suppressed during initial paint)
 10. Wire Shift+Tab capture → send CSI Z via 'terminal-input'
 11. Wire drag-drop + paste handlers → dispatch 'file-upload'
 12. On touch devices: suppress touchend auto-focus, wire beforeinput → terminal-input
 13. Set up ResizeObserver → fitTerminal()
 14. Dispatch 'terminal-ready'

attributeChangedCallback:
  - Update terminal options live (font-size, scrollback, etc.)
  - Re-fit if dimensions-related attribute changed

disconnectedCallback:
  1. Disconnect ResizeObserver
  2. Clear all debounce/reveal timers
  3. Dispose ghostty-web Terminal
  4. Remove internal DOM
```

#### Mobile Touch Device Support

The component detects touch devices (`'ontouchstart' in window || navigator.maxTouchPoints > 0`) and applies mobile-specific behavior:

**Virtual Keyboard (VKB) input path:**
ghostty-web marks its parent div as `contenteditable="true"`. On mobile, tapping focuses the contenteditable div rather than ghostty-web's internal textarea, so ghostty-web's InputHandler never sees keystrokes. The component intercepts `beforeinput` events on the contenteditable host div and dispatches them as `terminal-input` events:

- `insertText` / `insertReplacementText` → typed text (newlines → `\r`)
- `insertLineBreak` / `insertParagraph` → `\r` (Enter)
- `deleteContentBackward` → `\x7f` (Backspace)
- `deleteContentForward` → `\x1b[3~` (Delete)

**VKB lifecycle:**
The component does NOT manage VKB open/close — that's the consumer's job via `focusTextarea()` / `blurTextarea()`. The Gastown Dashboard sample puts a keyboard toggle button (⌨) in the header bar that calls these methods.

**Resize flicker suppression on touch:**
On desktop, `#markResizePending()` hides the host div during resize to prevent flicker. On touch devices this is skipped — hiding the host blurs the textarea, which dismisses the VKB, causing an infinite resize loop (VKB dismiss → resize → VKB open → resize...).

**Touch event suppression:**
`touchend` events on the host div are stopped via `stopPropagation()` to prevent ghostty-web's auto-focus on touch, which would pop the VKB on every tap including scrolls.

**Auto-focus suppression:**
`focus()` skips `terminal.focus()` on touch devices. VKB is controlled externally via `focusTextarea()` / `blurTextarea()`.

##### Outstanding Mobile Issues

The following are known issues to be fixed in future work:

1. **No touch scrolling** — Terminal content cannot be scrolled by touch gesture. Need to investigate ghostty-web's touch scroll support and potentially add touch event handling.

2. **Header scrolls with VKB** — When the virtual keyboard appears, the header scrolls up off-screen instead of staying pinned. The `visualViewport` resize handler adjusts container height, but the header needs to be anchored independently of the scroll.

3. **Audio input positioning** — During voice recording (speech-to-text), the audio input UI appears in an unexpected position just above the keyboard instead of near the input area.

#### Framework Integration

The element works everywhere custom elements work — which is everywhere.

**Vanilla HTML/JS:**
```html
<tmux-adapter-web id="term" name="hq-mayor"></tmux-adapter-web>

<script type="module">
const { encodeBinaryFrame } = await import('http://your-adapter:8080/tmux-adapter-web/index.js');

const el = document.getElementById('term');
el.addEventListener('terminal-input', (e) => {
  ws.send(encodeBinaryFrame(0x02, e.detail.name, e.detail.data));
});
el.addEventListener('terminal-resize', (e) => {
  ws.send(encodeBinaryFrame(0x03, e.detail.name,
    textEncoder.encode(e.detail.cols + ':' + e.detail.rows)));
});
el.addEventListener('file-upload', (e) => {
  ws.send(encodeBinaryFrame(0x04, e.detail.name, e.detail.payload));
});

// When binary output arrives from server:
el.write(outputBytes);
</script>
```

**React:**
```jsx
function AgentTerminal({ name, onInput, onResize, onFileUpload, termRef }) {
  return (
    <tmux-adapter-web
      ref={termRef}
      name={name}
      font-size="13"
      min-cols="80"
      onterminal-input={onInput}
      onterminal-resize={onResize}
      onfile-upload={onFileUpload}
    />
  );
}
```

**Vue:**
```vue
<tmux-adapter-web
  ref="term"
  :name="selectedAgent"
  font-size="13"
  @terminal-input="onInput"
  @terminal-resize="onResize"
  @file-upload="onFileUpload"
/>
```

**Svelte:**
```svelte
<tmux-adapter-web
  bind:this={termEl}
  name={selectedAgent}
  font-size="13"
  on:terminal-input={onInput}
  on:terminal-resize={onResize}
  on:file-upload={onFileUpload}
/>
```

### `protocol.js` — Standalone Binary Helpers

Exported separately for consumers who want binary encode/decode without the
terminal UI (e.g., a headless Node.js client):

```js
import { encodeBinaryFrame, decodeBinaryFrame, BinaryMsgType }
  from 'http://your-adapter:8080/tmux-adapter-web/protocol.js';

// Encode
const frame = encodeBinaryFrame(BinaryMsgType.KeyboardInput, 'agent-1', inputBytes);
ws.send(frame);

// Decode
const { msgType, agentName, payload } = decodeBinaryFrame(buffer);
if (msgType === BinaryMsgType.TerminalOutput) { ... }
```

`BinaryMsgType` constants:

| Name | Value | Direction |
|------|-------|-----------|
| `TerminalOutput` | `0x01` | server → client |
| `KeyboardInput` | `0x02` | client → server |
| `Resize` | `0x03` | client → server |
| `FileUpload` | `0x04` | client → server |

---

## 4. Key Design Decisions

### Custom Element (No Shadow DOM)

The component is a standard custom element (`customElements.define`), which
makes it a first-class HTML citizen usable from any framework or plain HTML.

However, it does **not** use Shadow DOM. ghostty-web measures the rendered DOM
to calculate cell dimensions (via `.xterm-screen` or `<canvas>` query). Shadow
DOM boundaries break these measurements — `querySelector` from inside the
shadow root can't find elements appended by ghostty-web's internal rendering,
and `offsetWidth`/`offsetHeight` behave differently across shadow boundaries.

The component injects scoped styles into the document head (once, guarded by a
check) and uses specific class names (`.tmux-adapter-web-host`) to avoid
collisions. This gives us encapsulation-by-convention without the measurement
breakage Shadow DOM would cause.

### Custom Fit Logic Instead of FitAddon

xterm.js FitAddon calculates columns from container width. But we need a
minimum column count (80) to prevent TUI applications from breaking on narrow
viewports. When the natural column count drops below 80, we:

1. Keep `term.resize(80, rows)` — the terminal thinks it's 80 columns wide
2. Set the host element's width to `80 * cellWidth` pixels
3. Apply `transform: scaleX(containerWidth / requiredWidth)` to shrink visually

This is ~40 lines of purpose-built logic. FitAddon doesn't support this
pattern, and wrapping it would be more complex than owning the fit code.

```
Container: 600px wide, cellWidth = 9px
Natural cols: floor(600/9) = 66 → below min-cols (80)

Strategy:
  term.resize(80, rows)              // terminal is 80 cols
  host.style.width = 720px           // 80 × 9px
  host.style.transform = scaleX(0.833) // 600/720, visually fits container
```

### ghostty-web as Peer Dependency

The consumer must `import { init, Terminal } from 'ghostty-web'` and call
`await init()` before any `<tmux-adapter-web>` elements connect to the DOM.
This avoids:

- Double-loading the ~400KB WASM blob
- Version conflicts between the component and consumer
- Import map complexity

The element imports `Terminal` from the same ghostty-web module the consumer
already loaded (ES module singleton behavior). If `init()` hasn't been called,
`connectedCallback` throws.

### No Build Step

Vanilla ES modules, no TypeScript, no bundler. Files are authored as `.js` and
imported directly. This matches the existing `index.html` approach and avoids
build tooling for what is ultimately a few hundred lines of code.

### Binary Protocol as Separate Export

`protocol.js` can be used independently of the terminal UI — e.g., a headless
Node.js client that speaks the binary protocol without rendering terminals.
Keeping it separate also makes the dependency graph clear:
`tmux-adapter-web.js` imports from `protocol.js` and `fit.js`, but
`protocol.js` imports nothing.

### One Element = One Terminal

No pool manager, no `show(name)`/`hideAll()` orchestration. Each
`<tmux-adapter-web>` element is a self-contained terminal. The consumer
manages which elements exist in the DOM, and the platform handles the rest:

- **Show an agent:** create/show the element
- **Hide an agent:** hide/remove the element
- **Switch agents:** hide one, show another (or remove + create)
- **Pool terminals:** just keep the elements in the DOM, toggle `display`

This is the natural model for every framework and for vanilla HTML. The
component doesn't need to know about multi-terminal orchestration because
the DOM already is an orchestrator.

---

## 5. Module Breakdown

### `tmux-adapter-web.js` — Custom Element Definition

The main module. Defines and exports the `TmuxAdapterWeb` class (extends
`HTMLElement`). Importing `index.js` auto-registers it as `<tmux-adapter-web>`.

| Responsibility | Current `index.html` lines | Approx. lines |
|---------------|---------------------------|---------------|
| `connectedCallback` — terminal creation, event wiring | 634–695 | ~60 |
| Initial-paint + resize-pending state machine | 462–520, 906–937 | ~60 |
| Scroll preservation (`getViewportY`, `isAtBottom`, sticky write) | 828–951 | ~30 |
| Shift+Tab capture | 658–668 | ~10 |
| `onData` → `terminal-input`, `onResize` → `terminal-resize` | 671–686 | ~15 |
| `disconnectedCallback` — cleanup | (new) | ~20 |
| `attributeChangedCallback` — live updates | (new) | ~15 |
| `write()` method — output + paint gating + scroll | 902–951 | ~25 |
| Style injection (once) | CSS 187–220 | ~20 |
| **Total** | | **~255** |

### `fit.js` — Custom Fit Logic

Terminal fitting with min-cols enforcement and scaleX scaling. Used internally
by the custom element.

| Responsibility | Current `index.html` lines | Approx. lines |
|---------------|---------------------------|---------------|
| `getTerminalScreen()` | 578–581 | ~5 |
| `fitTerminal()` with min-cols + scaleX | 583–621 | ~40 |
| **Total** | | **~45** |

### `file-transfer.js` — File Upload Handling

Drag-drop, paste handling, and binary `0x04` payload construction. Used
internally by the custom element.

| Responsibility | Current `index.html` lines | Approx. lines |
|---------------|---------------------------|---------------|
| `encodeFilePayload()` — build binary payload | 723–748 | ~25 |
| `hasFiles()` — DataTransfer check | 750–756 | ~7 |
| `wireFileTransferHandlers()` — drag/drop/paste DOM events | 758–800 | ~40 |
| **Total** | | **~72** |

### `protocol.js` — Binary Frame Encode/Decode

Stateless binary protocol helpers. No terminal or DOM dependency. Usable in
Node.js or any JS environment.

| Responsibility | Current `index.html` lines | Approx. lines |
|---------------|---------------------------|---------------|
| `encodeBinaryFrame()` (was `sendBinary`) | 803–812 | ~10 |
| `decodeBinaryFrame()` (was `parseBinaryMessage`) | 814–826 | ~15 |
| `BinaryMsgType` constants (`0x01`–`0x04`) | 440–444 | ~5 |
| **Total** | | **~30** |

### `index.js` — Barrel + Auto-Registration

```js
import { TmuxAdapterWeb } from './tmux-adapter-web.js';
export { TmuxAdapterWeb } from './tmux-adapter-web.js';
export { encodeBinaryFrame, decodeBinaryFrame, BinaryMsgType } from './protocol.js';

customElements.define('tmux-adapter-web', TmuxAdapterWeb);
```

Importing this module registers the element. Consumers who want manual
registration can import `tmux-adapter-web.js` directly.

---

## 6. Consumer Integration

See `samples/index.html` for a complete working example. The sample uses
`?adapter=host:port` to dynamically import the component from the adapter and
connect the WebSocket — one parameter controls everything.

The key wiring is minimal — the consumer listens for events from
`<tmux-adapter-web>` elements and forwards them over the WebSocket, and routes
incoming binary frames to the appropriate element's `write()` method.

| Concern | Consumer responsibility |
|---------|----------------------|
| Terminal creation + lifecycle | `createElement` / `.remove()` |
| Custom fit + scaleX | Handled by element |
| Resize debounce + flicker prevention | Handled by element |
| File upload encoding + drag/drop | Listen for `file-upload` event, send binary frame |
| Scroll preservation | Handled by element |
| Binary framing | `import { encodeBinaryFrame, decodeBinaryFrame }` |
| Terminal pooling | DOM is the pool — toggle `display` or add/remove elements |
| Mobile keyboard input translation | Handled by element (`beforeinput` → `terminal-input`) |
| Mobile VKB open/close | Consumer calls `focusTextarea()` / `blurTextarea()` |

The consumer keeps full control over transport (WebSocket), agent lifecycle
(JSON messages), UI (sidebar, header, mobile drawer), and VKB toggle. The
terminal hosting complexity lives entirely inside `<tmux-adapter-web>`.

---

## Related Specs

- [Terminal Features](terminal-features.md) — ghostty-web architecture, binary
  protocol, scroll/keyboard semantics that `<tmux-adapter-web>` implements
- [Adapter API](adapter-api.md) — WebSocket message format and binary frame
  layout that consumers wire to `<tmux-adapter-web>` events
