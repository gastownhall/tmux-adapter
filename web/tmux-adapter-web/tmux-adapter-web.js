// <tmux-adapter-web> custom element — a self-contained ghostty-web terminal host.
// One element = one terminal. Handles fit, resize, scroll preservation, file upload,
// and initial-paint gating. Transport-agnostic: dispatches events, never touches WebSocket.

import { Terminal } from 'ghostty-web';
import { fitTerminal, getTerminalScreen } from './fit.js';
import { wireFileTransferHandlers } from './file-transfer.js';

var STYLE_ID = 'tmux-adapter-web-styles';
var RESIZE_SEND_DEBOUNCE_MS = 90;
var RESIZE_REVEAL_TIMEOUT_MS = 260;
var INITIAL_PAINT_TIMEOUT_MS = 3000;

var COMPONENT_CSS = [
  'tmux-adapter-web { display: block; width: 100%; height: 100%; overflow: hidden; position: relative; }',
  '.tmux-adapter-web-host { width: 100%; height: 100%; transform-origin: top left; will-change: transform; }',
  'tmux-adapter-web.drag-over::after {',
  "  content: 'Drop files to upload and paste';",
  '  position: absolute;',
  '  inset: 16px;',
  '  border: 2px dashed #58a6ff;',
  '  border-radius: 8px;',
  '  background: rgba(13, 17, 23, 0.85);',
  '  color: #58a6ff;',
  '  font-size: 13px;',
  '  display: flex;',
  '  align-items: center;',
  '  justify-content: center;',
  '  pointer-events: none;',
  '  z-index: 5;',
  '}',
  '@media (max-width: 900px) {',
  '  tmux-adapter-web.drag-over::after { inset: 10px; font-size: 12px; }',
  '}'
].join('\n');

function injectStyles() {
  if (document.getElementById(STYLE_ID)) return;
  var style = document.createElement('style');
  style.id = STYLE_ID;
  style.textContent = COMPONENT_CSS;
  document.head.appendChild(style);
}

function getViewportY(term) {
  if (typeof term.getViewportY === 'function') return term.getViewportY();
  if (term.buffer && term.buffer.active && typeof term.buffer.active.viewportY === 'number') {
    return term.buffer.active.viewportY;
  }
  return 0;
}

function getBottomY(term) {
  if (typeof term.getViewportY === 'function') return 0;
  if (term.buffer && term.buffer.active && typeof term.buffer.active.baseY === 'number') {
    return term.buffer.active.baseY;
  }
  return 0;
}

function isAtBottom(term, y) {
  return Math.abs(y - getBottomY(term)) < 0.5;
}

var textEncoder = new TextEncoder();

export class TmuxAdapterWeb extends HTMLElement {

  static get observedAttributes() {
    return ['name', 'font-size', 'scrollback', 'cursor-blink', 'min-cols', 'theme-background'];
  }

  #terminal = null;
  #host = null;
  #resizeObserver = null;
  #fileTransferCleanup = null;

  // State machine flags
  #pendingInitialPaint = false;
  #pendingSnapshotReveal = false;
  #pendingResizePaint = false;

  // Timer IDs
  #initialPaintTimer = null;
  #resizeSendTimer = null;
  #resizeRevealTimer = null;

  get cols() { return this.#terminal ? this.#terminal.cols : 0; }
  get rows() { return this.#terminal ? this.#terminal.rows : 0; }

  connectedCallback() {
    injectStyles();

    // Create internal host div
    this.#host = document.createElement('div');
    this.#host.className = 'tmux-adapter-web-host';
    this.appendChild(this.#host);

    // CRITICAL: set pendingInitialPaint BEFORE terminal.open() —
    // open() may trigger onResize which must be suppressed.
    this.#pendingInitialPaint = true;

    // Create terminal with attribute values
    this.#terminal = new Terminal({
      fontSize: this.#getNumAttr('font-size', 13),
      scrollback: this.#getNumAttr('scrollback', 10000),
      cursorBlink: this.hasAttribute('cursor-blink'),
      theme: { background: this.getAttribute('theme-background') || '#0d1117' },
    });

    this.#terminal.open(this.#host);

    // Hide cursor at origin until app positions it via escape sequences
    this.#terminal.write('\x1b[?25l');

    // Hide host until first write reveals it
    this.#host.style.visibility = 'hidden';

    // Safety timeout for initial paint
    this.#initialPaintTimer = setTimeout(() => {
      this.#initialPaintTimer = null;
      if (!this.#pendingInitialPaint) return;
      this.#pendingInitialPaint = false;
      this.#host.style.visibility = '';
      this.#fit();
      this.#terminal.focus();
    }, INITIAL_PAINT_TIMEOUT_MS);

    // Wire onData -> dispatch terminal-input
    this.#terminal.onData((data) => {
      if (typeof this.#terminal.scrollToBottom === 'function') {
        this.#terminal.scrollToBottom();
      }
      var payload = textEncoder.encode(data);
      this.dispatchEvent(new CustomEvent('terminal-input', {
        bubbles: true,
        detail: { name: this.getAttribute('name') || '', data: payload }
      }));
    });

    // Wire onResize -> debounced terminal-resize dispatch
    // CRITICAL: return early if pendingInitialPaint to prevent SIGWINCH cascade
    this.#terminal.onResize((size) => {
      if (this.#pendingInitialPaint) return;
      this.#markResizePending();
      this.#scheduleResizeSend(size.cols, size.rows);
    });

    // Shift+Tab capture — send CSI Z instead of browser focus traversal
    if (typeof this.#terminal.attachCustomKeyEventHandler === 'function') {
      this.#terminal.attachCustomKeyEventHandler((ev) => {
        if (ev.key === 'Tab' && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
          ev.preventDefault();
          ev.stopPropagation();
          this.dispatchEvent(new CustomEvent('terminal-input', {
            bubbles: true,
            detail: { name: this.getAttribute('name') || '', data: new Uint8Array([0x1b, 0x5b, 0x5a]) }
          }));
          return true;
        }
        return false;
      });
    }

    // Wire file transfer handlers
    var self = this;
    this.#fileTransferCleanup = wireFileTransferHandlers(
      this,           // wrapperEl — drag events on the custom element itself
      this,           // dispatchTarget — events bubble from this element
      function() { return self.getAttribute('name') || ''; }
    );

    // ResizeObserver watches the custom element itself
    this.#resizeObserver = new ResizeObserver(() => { this.#fit(); });
    this.#resizeObserver.observe(this);

    // Dispatch terminal-ready
    this.dispatchEvent(new CustomEvent('terminal-ready', {
      bubbles: true,
      detail: { name: this.getAttribute('name') || '' }
    }));
  }

  disconnectedCallback() {
    // Clear all timers
    if (this.#initialPaintTimer) { clearTimeout(this.#initialPaintTimer); this.#initialPaintTimer = null; }
    if (this.#resizeSendTimer) { clearTimeout(this.#resizeSendTimer); this.#resizeSendTimer = null; }
    if (this.#resizeRevealTimer) { clearTimeout(this.#resizeRevealTimer); this.#resizeRevealTimer = null; }

    // Disconnect ResizeObserver
    if (this.#resizeObserver) { this.#resizeObserver.disconnect(); this.#resizeObserver = null; }

    // File transfer cleanup
    if (this.#fileTransferCleanup) { this.#fileTransferCleanup(); this.#fileTransferCleanup = null; }

    // Dispose terminal
    if (this.#terminal) {
      if (typeof this.#terminal.dispose === 'function') this.#terminal.dispose();
      this.#terminal = null;
    }

    // Remove host div
    if (this.#host && this.#host.parentNode) { this.#host.remove(); this.#host = null; }
  }

  attributeChangedCallback(attr, oldVal, newVal) {
    if (!this.#terminal || oldVal === newVal) return;

    // Font size and scrollback require terminal option updates
    // ghostty-web Terminal options are set at construction; for live updates
    // the simplest correct approach is to refit after attribute changes.
    if (attr === 'font-size' || attr === 'scrollback' || attr === 'min-cols') {
      this.#fit();
    }
  }

  // --- Public methods ---

  write(data) {
    if (!this.#terminal) return;

    // Snapshot reveal: reset() set the flag, now we write data and schedule reveal
    if (this.#pendingSnapshotReveal) {
      this.#terminal.write(data);
      this.#pendingSnapshotReveal = false;
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          this.#finishInitialPaint();
          this.#finishResizePending();
          this.#host.style.visibility = '';
          this.#fit();
          this.#terminal.focus();
        });
      });
      return;
    }

    // Initial paint: write silently, host stays hidden
    if (this.#pendingInitialPaint) {
      this.#terminal.write(data);
      return;
    }

    // Resize pending: write silently, safety timeout handles reveal
    if (this.#pendingResizePaint) {
      this.#terminal.write(data);
      return;
    }

    // Normal write with scroll preservation
    var savedY = getViewportY(this.#terminal);
    var atBottom = isAtBottom(this.#terminal, savedY);
    this.#terminal.write(data);

    if (!atBottom && typeof this.#terminal.scrollToLine === 'function') {
      this.#terminal.scrollToLine(savedY);
    }
  }

  reset() {
    if (!this.#terminal) return;
    this.#host.style.visibility = 'hidden';
    if (typeof this.#terminal.reset === 'function') {
      this.#terminal.reset();
    } else if (typeof this.#terminal.clear === 'function') {
      this.#terminal.clear();
    }
    this.#terminal.write('\x1b[?25l');
    this.#pendingSnapshotReveal = true;
  }

  focus() {
    if (this.#terminal) this.#terminal.focus();
  }

  // --- Internal methods ---

  #getNumAttr(name, defaultVal) {
    var val = this.getAttribute(name);
    if (val === null) return defaultVal;
    var num = Number(val);
    return isNaN(num) ? defaultVal : num;
  }

  #fit() {
    if (!this.#terminal || !this.#host) return;
    fitTerminal(this.#terminal, this, this.#host, this.#getNumAttr('min-cols', 80));
  }

  #finishInitialPaint() {
    this.#pendingInitialPaint = false;
    if (this.#initialPaintTimer) {
      clearTimeout(this.#initialPaintTimer);
      this.#initialPaintTimer = null;
    }
  }

  #finishResizePending() {
    if (!this.#pendingResizePaint) return;
    this.#pendingResizePaint = false;
    if (this.#resizeRevealTimer) {
      clearTimeout(this.#resizeRevealTimer);
      this.#resizeRevealTimer = null;
    }
    this.#host.style.visibility = '';
  }

  #markResizePending() {
    if (this.#pendingInitialPaint) return;
    this.#pendingResizePaint = true;
    this.#host.style.visibility = 'hidden';

    if (this.#resizeRevealTimer) clearTimeout(this.#resizeRevealTimer);
    this.#resizeRevealTimer = setTimeout(() => {
      this.#resizeRevealTimer = null;
      this.#pendingResizePaint = false;
      this.#host.style.visibility = '';
      this.#fit();
    }, RESIZE_REVEAL_TIMEOUT_MS);
  }

  #scheduleResizeSend(cols, rows) {
    if (this.#resizeSendTimer) clearTimeout(this.#resizeSendTimer);
    this.#resizeSendTimer = setTimeout(() => {
      this.#resizeSendTimer = null;
      this.dispatchEvent(new CustomEvent('terminal-resize', {
        bubbles: true,
        detail: { name: this.getAttribute('name') || '', cols: cols, rows: rows }
      }));
    }, RESIZE_SEND_DEBOUNCE_MS);
  }
}
