// <tmux-converter-web> custom element — a self-contained conversation viewer.
// One element = one conversation stream. Handles event rendering, filtering,
// file attachments, lightbox, drag-drop, and auto-scroll.
// Transport-agnostic: dispatches events, never touches WebSocket.

import { clearChildren, formatTime, truncate } from '../shared/utils.js';

var STYLE_ID = 'tmux-converter-web-styles';
var MAX_RENDER_EVENTS = 2000;
var MAX_UPLOAD_BYTES = 8 * 1024 * 1024;

var COMPONENT_CSS = `
tmux-converter-web {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  overflow: hidden;
  position: relative;
}

tmux-converter-web .events-wrap {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

tmux-converter-web .placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #484f58;
  font-size: 14px;
}

tmux-converter-web .snapshot-divider {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 16px 0;
  font-size: 11px;
  color: #484f58;
  text-transform: uppercase;
  letter-spacing: 1px;
}

tmux-converter-web .snapshot-divider::before,
tmux-converter-web .snapshot-divider::after {
  content: '';
  flex: 1;
  border-top: 1px dashed #30363d;
}

tmux-converter-web .event-block {
  margin-bottom: 8px;
  border-radius: 8px;
  padding: 10px 14px;
  font-size: 13px;
  line-height: 1.5;
  border-left: 3px solid transparent;
  position: relative;
}

tmux-converter-web .event-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 11px;
  color: #8b949e;
}

tmux-converter-web .event-type-badge {
  font-size: 10px;
  font-weight: 700;
  padding: 1px 6px;
  border-radius: 4px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

tmux-converter-web .event-seq {
  color: #484f58;
  font-size: 10px;
}

tmux-converter-web .event-time {
  color: #484f58;
  font-size: 10px;
  margin-left: auto;
}

tmux-converter-web .event-content {
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 400px;
  overflow-y: auto;
}

tmux-converter-web .event-content::-webkit-scrollbar { width: 4px; }
tmux-converter-web .event-content::-webkit-scrollbar-track { background: transparent; }
tmux-converter-web .event-content::-webkit-scrollbar-thumb { background: #30363d; border-radius: 2px; }

/* Event type styles */
tmux-converter-web .event-block.user { background: #0d2137; border-left-color: #58a6ff; }
tmux-converter-web .event-block.user .event-type-badge { background: #1f6feb33; color: #58a6ff; }

tmux-converter-web .event-block.assistant { background: #0d2117; border-left-color: #3fb950; }
tmux-converter-web .event-block.assistant .event-type-badge { background: #23883333; color: #3fb950; }

tmux-converter-web .event-block.tool_use { background: #261d0d; border-left-color: #d29922; }
tmux-converter-web .event-block.tool_use .event-type-badge { background: #d2992233; color: #d29922; }

tmux-converter-web .event-block.tool_result { background: #261a0d; border-left-color: #f0883e; }
tmux-converter-web .event-block.tool_result .event-type-badge { background: #f0883e33; color: #f0883e; }

tmux-converter-web .event-block.thinking { background: #1a0d26; border-left-color: #a371f7; }
tmux-converter-web .event-block.thinking .event-type-badge { background: #a371f733; color: #a371f7; }

tmux-converter-web .event-block.system { background: #161b22; border-left-color: #8b949e; }
tmux-converter-web .event-block.system .event-type-badge { background: #8b949e33; color: #8b949e; }

tmux-converter-web .event-block.progress { background: #0d1f26; border-left-color: #79c0ff; }
tmux-converter-web .event-block.progress .event-type-badge { background: #79c0ff33; color: #79c0ff; }

tmux-converter-web .event-block.error { background: #260d0d; border-left-color: #f85149; }
tmux-converter-web .event-block.error .event-type-badge { background: #f8514933; color: #f85149; }

tmux-converter-web .event-block.turn_end { background: #161b22; border-left-color: #484f58; }
tmux-converter-web .event-block.turn_end .event-type-badge { background: #484f5833; color: #484f58; }

tmux-converter-web .event-block.queue_op { background: #161b22; border-left-color: #484f58; }
tmux-converter-web .event-block.queue_op .event-type-badge { background: #484f5833; color: #484f58; }

tmux-converter-web .tool-name {
  font-weight: 600;
  color: #d29922;
  font-size: 12px;
}

tmux-converter-web .tool-input {
  background: #0d1117;
  border-radius: 4px;
  padding: 6px 8px;
  margin-top: 4px;
  font-size: 11px;
  color: #8b949e;
  max-height: 150px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-word;
}

tmux-converter-web .tool-output {
  background: #0d1117;
  border-radius: 4px;
  padding: 6px 8px;
  margin-top: 4px;
  font-size: 11px;
  color: #8b949e;
  max-height: 200px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-word;
}

tmux-converter-web .token-usage {
  font-size: 10px;
  color: #484f58;
  margin-top: 4px;
}

tmux-converter-web .switch-notice {
  text-align: center;
  padding: 12px;
  color: #58a6ff;
  font-size: 12px;
  font-weight: 600;
  border: 1px dashed #1f6feb;
  border-radius: 8px;
  margin: 16px 0;
  background: #0d111733;
}

tmux-converter-web .event-count-info {
  text-align: center;
  padding: 8px;
  color: #484f58;
  font-size: 11px;
  margin-bottom: 8px;
}

/* Filter bar */
tmux-converter-web .filter-bar {
  display: flex;
  gap: 6px;
  padding: 8px 16px;
  background: #161b22;
  border-bottom: 1px solid #30363d;
  flex-shrink: 0;
  flex-wrap: wrap;
  align-items: center;
}

tmux-converter-web .filter-bar label {
  font-size: 11px;
  color: #8b949e;
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
}

tmux-converter-web .filter-bar input[type="checkbox"] {
  accent-color: #58a6ff;
}

tmux-converter-web .filter-label {
  font-size: 11px;
  color: #484f58;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-right: 4px;
}

/* Input area */
tmux-converter-web .input-area {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding: 0;
  border-top: 1px solid #30363d;
  background: #161b22;
  flex-shrink: 0;
}

tmux-converter-web .attachments {
  display: flex;
  gap: 8px;
  padding: 10px 16px 0;
  flex-wrap: wrap;
}

tmux-converter-web .attachment-thumb {
  position: relative;
  width: 64px;
  height: 64px;
  border-radius: 6px;
  overflow: hidden;
  border: 1px solid #30363d;
  cursor: pointer;
  background: #0d1117;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}

tmux-converter-web .attachment-thumb:hover { border-color: #58a6ff; }

tmux-converter-web .attachment-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

tmux-converter-web .attachment-thumb .file-icon {
  font-size: 11px;
  color: #8b949e;
  text-align: center;
  padding: 4px;
  word-break: break-all;
  line-height: 1.2;
  overflow: hidden;
  max-height: 100%;
}

tmux-converter-web .attachment-remove {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: rgba(248, 81, 73, 0.9);
  color: #fff;
  border: none;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  opacity: 0;
  transition: opacity 0.1s;
}

tmux-converter-web .attachment-thumb:hover .attachment-remove { opacity: 1; }

tmux-converter-web .input-row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 12px 16px;
}

tmux-converter-web .prompt-input {
  flex: 1;
  background: #0d1117;
  color: #c9d1d9;
  border: 1px solid #30363d;
  border-radius: 6px;
  padding: 8px 12px;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.5;
  resize: none;
  min-height: 36px;
  max-height: 150px;
  overflow-y: auto;
}

tmux-converter-web .prompt-input:focus {
  outline: none;
  border-color: #58a6ff;
}

tmux-converter-web .prompt-input::placeholder {
  color: #484f58;
}

tmux-converter-web .send-btn {
  background: #238636;
  color: #fff;
  border: none;
  border-radius: 6px;
  padding: 8px 16px;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
}

tmux-converter-web .send-btn:hover { background: #2ea043; }
tmux-converter-web .send-btn:active { background: #1a7f37; }
tmux-converter-web .send-btn.sending { opacity: 0.6; pointer-events: none; }

/* Lightbox */
tmux-converter-web .lightbox {
  position: fixed;
  inset: 0;
  background: rgba(1, 4, 9, 0.85);
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
}

tmux-converter-web .lightbox img {
  max-width: 90vw;
  max-height: 90vh;
  border-radius: 8px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.6);
}

tmux-converter-web .lightbox .file-preview {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 24px 32px;
  color: #c9d1d9;
  font-size: 14px;
  text-align: center;
  max-width: 400px;
}

tmux-converter-web .lightbox .file-preview .filename { font-weight: 600; margin-bottom: 8px; }
tmux-converter-web .lightbox .file-preview .filesize { color: #8b949e; font-size: 12px; }

/* Drag-over overlay */
tmux-converter-web.drag-over .events-wrap {
  background: #0d2137;
  outline: 2px dashed #58a6ff;
  outline-offset: -2px;
}

/* Runtime badge colors */
tmux-converter-web .badge-runtime { color: #8b949e; }
tmux-converter-web .badge-runtime.claude { color: #a371f7; }
tmux-converter-web .badge-runtime.codex { color: #3fb950; }
tmux-converter-web .badge-runtime.gemini { color: #58a6ff; }

tmux-converter-web .header-conv-id {
  color: #484f58;
  font-size: 11px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Loading spinner */
tmux-converter-web .agent-list-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 56px;
  padding: 12px;
  color: #8b949e;
  font-size: 12px;
  text-align: center;
}

tmux-converter-web .spinner {
  width: 12px;
  height: 12px;
  border: 2px solid #30363d;
  border-top-color: #58a6ff;
  border-radius: 50%;
  animation: tmux-converter-spin 0.8s linear infinite;
  flex-shrink: 0;
}

@keyframes tmux-converter-spin {
  to { transform: rotate(360deg); }
}

tmux-converter-web .progress-bar {
  width: 200px;
  height: 4px;
  background: #30363d;
  border-radius: 2px;
  overflow: hidden;
}

tmux-converter-web .progress-fill {
  height: 100%;
  background: #58a6ff;
  border-radius: 2px;
  transition: width 0.15s ease-out;
}
`;

function injectStyles() {
  if (document.getElementById(STYLE_ID)) return;
  var style = document.createElement('style');
  style.id = STYLE_ID;
  style.textContent = COMPONENT_CSS;
  document.head.appendChild(style);
}

function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

export class TmuxConverterWeb extends HTMLElement {

  static get observedAttributes() {
    return ['name'];
  }

  // Internal DOM refs
  #filterBar = null;
  #eventsWrap = null;
  #inputArea = null;
  #attachmentsEl = null;
  #promptInput = null;
  #sendBtn = null;

  // Filter checkboxes
  #showThinkingEl = null;
  #showProgressEl = null;
  #showToolInputEl = null;
  #showToolOutputEl = null;

  // State
  #eventStore = [];
  #liveEventCount = 0;
  #autoScroll = true;
  #attachments = []; // { id, file, url }
  #nextAttachId = 0;
  #dragDepth = 0;

  get liveEventCount() { return this.#liveEventCount; }

  connectedCallback() {
    injectStyles();
    this.#buildDOM();
    this.#wireEvents();
  }

  disconnectedCallback() {
    // Revoke object URLs for attachments
    this.#attachments.forEach(function(a) { if (a.url) URL.revokeObjectURL(a.url); });
    this.#attachments = [];
  }

  attributeChangedCallback() {
    // name changes are read dynamically via getAttribute
  }

  // --- Public methods ---

  setEvents(events, isSnapshot) {
    this.#eventStore = events.slice();
    this.#liveEventCount = 0;
    this.#renderEvents(this.#eventStore, isSnapshot);
  }

  appendEvent(event) {
    this.#eventStore.push(event);
    this.#liveEventCount++;
    this.#appendEventToDOM(event);
    this.#scrollToBottom(false);
  }

  clear() {
    this.#eventStore = [];
    this.#liveEventCount = 0;
    clearChildren(this.#eventsWrap);
    var placeholder = document.createElement('div');
    placeholder.className = 'placeholder';
    placeholder.textContent = 'No events yet';
    this.#eventsWrap.appendChild(placeholder);
    this.#autoScroll = true;
  }

  showWaiting(message) {
    this.#autoScroll = true;
    this.#eventsWrap.style.scrollBehavior = 'auto';
    clearChildren(this.#eventsWrap);
    var loading = document.createElement('div');
    loading.className = 'agent-list-state';
    var spinner = document.createElement('span');
    spinner.className = 'spinner';
    loading.appendChild(spinner);
    var text = document.createElement('span');
    text.textContent = message || 'Loading conversation...';
    loading.appendChild(text);
    this.#eventsWrap.appendChild(loading);
  }

  showProgress(loaded, total) {
    this.#autoScroll = true;
    var indeterminate = !total;
    var labelText = indeterminate
      ? 'Loading ' + loaded.toLocaleString() + ' events...'
      : 'Loading ' + loaded.toLocaleString() + ' of ' + total.toLocaleString() + ' events...';

    var existing = this.#eventsWrap.querySelector('.snapshot-progress');
    if (existing) {
      existing.querySelector('.progress-text').textContent = labelText;
      var fill = existing.querySelector('.progress-fill');
      if (fill) {
        if (indeterminate) {
          fill.parentElement.style.display = 'none';
        } else {
          fill.parentElement.style.display = '';
          fill.style.width = Math.round(loaded / total * 100) + '%';
        }
      }
      return;
    }

    clearChildren(this.#eventsWrap);
    var wrap = document.createElement('div');
    wrap.className = 'agent-list-state snapshot-progress';
    wrap.style.flexDirection = 'column';
    wrap.style.gap = '8px';
    var spinner = document.createElement('span');
    spinner.className = 'spinner';
    wrap.appendChild(spinner);
    var text = document.createElement('span');
    text.className = 'progress-text';
    text.textContent = labelText;
    wrap.appendChild(text);
    if (!indeterminate) {
      var bar = document.createElement('div');
      bar.className = 'progress-bar';
      var fill = document.createElement('div');
      fill.className = 'progress-fill';
      fill.style.width = Math.round(loaded / total * 100) + '%';
      bar.appendChild(fill);
      wrap.appendChild(bar);
    }
    this.#eventsWrap.appendChild(wrap);
  }

  showError(message) {
    clearChildren(this.#eventsWrap);
    var errDiv = document.createElement('div');
    errDiv.className = 'event-count-info';
    errDiv.style.color = '#f85149';
    errDiv.textContent = message || 'An error occurred';
    this.#eventsWrap.appendChild(errDiv);
  }

  addSwitchNotice(from, to) {
    var notice = document.createElement('div');
    notice.className = 'switch-notice';
    notice.textContent = 'Conversation switched';

    var details = document.createElement('div');
    details.style.fontSize = '10px';
    details.style.color = '#484f58';
    details.style.marginTop = '4px';
    details.appendChild(document.createTextNode(
      truncate(from || '?', 40) + ' \u2192 ' + truncate(to || '?', 40)
    ));
    notice.appendChild(details);

    this.#eventsWrap.appendChild(notice);
    this.#scrollToBottom(false);
  }

  showInputArea() {
    if (this.#inputArea) this.#inputArea.style.display = 'flex';
  }

  hideInputArea() {
    if (this.#inputArea) this.#inputArea.style.display = 'none';
  }

  showFilters() {
    if (this.#filterBar) this.#filterBar.style.display = 'flex';
  }

  hideFilters() {
    if (this.#filterBar) this.#filterBar.style.display = 'none';
  }

  clearAttachments() {
    this.#attachments.forEach(function(a) { if (a.url) URL.revokeObjectURL(a.url); });
    this.#attachments = [];
    this.#renderAttachments();
  }

  getFilters() {
    return {
      excludeThinking: this.#showThinkingEl ? !this.#showThinkingEl.checked : false,
      excludeProgress: this.#showProgressEl ? !this.#showProgressEl.checked : true
    };
  }

  // --- DOM construction ---

  #buildDOM() {
    // Filter bar
    this.#filterBar = document.createElement('div');
    this.#filterBar.className = 'filter-bar';
    this.#filterBar.style.display = 'none';

    var filterLabel = document.createElement('span');
    filterLabel.className = 'filter-label';
    filterLabel.textContent = 'Show:';
    this.#filterBar.appendChild(filterLabel);

    this.#showThinkingEl = this.#createCheckbox('Thinking', true);
    this.#showProgressEl = this.#createCheckbox('Progress', false);
    this.#showToolInputEl = this.#createCheckbox('Tool Input', true);
    this.#showToolOutputEl = this.#createCheckbox('Tool Output', true);

    this.#filterBar.appendChild(this.#wrapCheckbox(this.#showThinkingEl, 'Thinking'));
    this.#filterBar.appendChild(this.#wrapCheckbox(this.#showProgressEl, 'Progress'));
    this.#filterBar.appendChild(this.#wrapCheckbox(this.#showToolInputEl, 'Tool Input'));
    this.#filterBar.appendChild(this.#wrapCheckbox(this.#showToolOutputEl, 'Tool Output'));
    this.appendChild(this.#filterBar);

    // Events area
    this.#eventsWrap = document.createElement('div');
    this.#eventsWrap.className = 'events-wrap';
    var placeholder = document.createElement('div');
    placeholder.className = 'placeholder';
    placeholder.textContent = 'No events yet';
    this.#eventsWrap.appendChild(placeholder);
    this.appendChild(this.#eventsWrap);

    // Input area
    this.#inputArea = document.createElement('div');
    this.#inputArea.className = 'input-area';
    this.#inputArea.style.display = 'none';

    this.#attachmentsEl = document.createElement('div');
    this.#attachmentsEl.className = 'attachments';
    this.#attachmentsEl.style.display = 'none';
    this.#inputArea.appendChild(this.#attachmentsEl);

    var inputRow = document.createElement('div');
    inputRow.className = 'input-row';

    this.#promptInput = document.createElement('textarea');
    this.#promptInput.className = 'prompt-input';
    this.#promptInput.placeholder = 'Send a message...';
    this.#promptInput.rows = 1;
    inputRow.appendChild(this.#promptInput);

    this.#sendBtn = document.createElement('button');
    this.#sendBtn.className = 'send-btn';
    this.#sendBtn.title = 'Send (Enter)';
    this.#sendBtn.textContent = 'Send';
    inputRow.appendChild(this.#sendBtn);

    this.#inputArea.appendChild(inputRow);
    this.appendChild(this.#inputArea);
  }

  #createCheckbox(label, checked) {
    var cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = checked;
    return cb;
  }

  #wrapCheckbox(cb, label) {
    var lbl = document.createElement('label');
    lbl.appendChild(cb);
    lbl.appendChild(document.createTextNode(' ' + label));
    return lbl;
  }

  // --- Event wiring ---

  #wireEvents() {
    var self = this;

    // Scroll tracking for auto-scroll
    this.#eventsWrap.addEventListener('scroll', function() {
      var el = self.#eventsWrap;
      self.#autoScroll = (el.scrollHeight - el.scrollTop - el.clientHeight) < 50;
    });

    // Filter checkbox changes
    // Server-side filters (thinking, progress) dispatch filter-change for re-follow
    var serverFilterEls = [this.#showThinkingEl, this.#showProgressEl];
    serverFilterEls.forEach(function(el) {
      el.addEventListener('change', function() {
        self.dispatchEvent(new CustomEvent('filter-change', {
          bubbles: true,
          detail: self.getFilters()
        }));
      });
    });

    // Client-only filters (tool input, tool output) just re-render
    var clientFilterEls = [this.#showToolInputEl, this.#showToolOutputEl];
    clientFilterEls.forEach(function(el) {
      el.addEventListener('change', function() { self.#reRenderEvents(); });
    });

    // Auto-resize textarea
    this.#promptInput.addEventListener('input', function() {
      this.style.height = 'auto';
      this.style.height = Math.min(this.scrollHeight, 150) + 'px';
    });

    // Enter to send, Shift+Enter for newline
    this.#promptInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        self.#sendAll();
      }
    });

    // Send button
    this.#sendBtn.addEventListener('click', function() { self.#sendAll(); });

    // Drag-and-drop
    this.addEventListener('dragenter', function(ev) {
      if (!self.#hasFilesDT(ev.dataTransfer)) return;
      ev.preventDefault();
      self.#dragDepth++;
      self.classList.add('drag-over');
    });

    this.addEventListener('dragover', function(ev) {
      if (!self.#hasFilesDT(ev.dataTransfer)) return;
      ev.preventDefault();
      if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy';
    });

    this.addEventListener('dragleave', function(ev) {
      ev.preventDefault();
      self.#dragDepth = Math.max(0, self.#dragDepth - 1);
      if (self.#dragDepth === 0) self.classList.remove('drag-over');
    });

    this.addEventListener('drop', function(ev) {
      ev.preventDefault();
      self.#dragDepth = 0;
      self.classList.remove('drag-over');
      if (!self.#hasFilesDT(ev.dataTransfer)) return;
      self.#queueFiles(ev.dataTransfer.files);
    });

    // Clipboard paste for files
    this.addEventListener('paste', function(ev) {
      var files = ev.clipboardData && ev.clipboardData.files;
      if (!files || files.length === 0) return;
      ev.preventDefault();
      self.#queueFiles(files);
    });
  }

  // --- Event rendering ---

  #shouldShowEvent(e) {
    if (e.type === 'thinking' && this.#showThinkingEl && !this.#showThinkingEl.checked) return false;
    if (e.type === 'progress' && this.#showProgressEl && !this.#showProgressEl.checked) return false;
    return true;
  }

  #renderEvents(events, isSnapshot) {
    var wrap = this.#eventsWrap;

    clearChildren(wrap);

    if (events.length === 0) {
      var empty = document.createElement('div');
      empty.className = 'event-count-info';
      empty.textContent = 'No events yet \u2014 waiting for conversation activity...';
      wrap.appendChild(empty);
      this.#autoScroll = true;
      return;
    }

    var visible = events.filter(this.#shouldShowEvent.bind(this));

    if (isSnapshot && events.length > 0) {
      var info = document.createElement('div');
      info.className = 'event-count-info';
      if (visible.length > MAX_RENDER_EVENTS) {
        info.textContent = 'Showing last ' + MAX_RENDER_EVENTS.toLocaleString() + ' of ' + visible.length.toLocaleString() + ' events (' + events.length.toLocaleString() + ' total)';
      } else {
        info.textContent = visible.length.toLocaleString() + ' historic event' + (visible.length !== 1 ? 's' : '') + (visible.length !== events.length ? ' (' + events.length.toLocaleString() + ' total)' : '');
      }
      wrap.appendChild(info);
    }

    var renderSlice = visible.length > MAX_RENDER_EVENTS ? visible.slice(visible.length - MAX_RENDER_EVENTS) : visible;
    var frag = document.createDocumentFragment();
    var self = this;
    renderSlice.forEach(function(e) {
      frag.appendChild(self.#createEventBlock(e));
    });

    if (isSnapshot && events.length > 0) {
      var divider = document.createElement('div');
      divider.className = 'snapshot-divider';
      divider.textContent = 'live';
      frag.appendChild(divider);
    }

    wrap.appendChild(frag);
    this.#autoScroll = true;

    // Snapshot loads should land at bottom immediately (no animated jump from top).
    this.#scrollToBottom(Boolean(isSnapshot));
  }

  #reRenderEvents() {
    if (this.#eventStore.length === 0) return;
    var snapshotCount = this.#eventStore.length - this.#liveEventCount;
    this.#renderEvents(this.#eventStore.slice(0, snapshotCount), true);

    for (var i = snapshotCount; i < this.#eventStore.length; i++) {
      this.#appendEventToDOM(this.#eventStore[i]);
    }
    this.#scrollToBottom(false);
  }

  #appendEventToDOM(e) {
    if (!this.#shouldShowEvent(e)) return;

    // Remove "no events" or "waiting" placeholder if present
    var info = this.#eventsWrap.querySelector('.event-count-info');
    if (info && info.textContent.indexOf('No events yet') !== -1) {
      info.remove();
    }
    var placeholder = this.#eventsWrap.querySelector('.placeholder');
    if (placeholder) placeholder.remove();

    this.#eventsWrap.appendChild(this.#createEventBlock(e));
  }

  #createEventBlock(e) {
    var block = document.createElement('div');
    block.className = 'event-block ' + (e.type || 'system');

    // Header
    var header = document.createElement('div');
    header.className = 'event-header';

    var typeBadge = document.createElement('span');
    typeBadge.className = 'event-type-badge';
    typeBadge.textContent = e.type || '?';
    header.appendChild(typeBadge);

    if (e.role && e.role !== e.type) {
      var roleSpan = document.createElement('span');
      roleSpan.textContent = e.role;
      roleSpan.style.color = '#8b949e';
      header.appendChild(roleSpan);
    }

    if (e.model) {
      var modelSpan = document.createElement('span');
      modelSpan.textContent = e.model;
      modelSpan.style.color = '#484f58';
      modelSpan.style.fontSize = '10px';
      header.appendChild(modelSpan);
    }

    var seqSpan = document.createElement('span');
    seqSpan.className = 'event-seq';
    seqSpan.textContent = '#' + e.seq;
    header.appendChild(seqSpan);

    var timeSpan = document.createElement('span');
    timeSpan.className = 'event-time';
    timeSpan.textContent = formatTime(e.timestamp);
    header.appendChild(timeSpan);

    block.appendChild(header);

    // Content blocks
    var self = this;
    if (e.content && e.content.length > 0) {
      e.content.forEach(function(cb) {
        if (cb.type === 'text' && cb.text) {
          var textDiv = document.createElement('div');
          textDiv.className = 'event-content';
          textDiv.textContent = cb.text;
          block.appendChild(textDiv);
        } else if (cb.type === 'tool_use' || cb.toolName) {
          var toolDiv = document.createElement('div');
          var toolLabel = document.createElement('span');
          toolLabel.className = 'tool-name';
          toolLabel.textContent = cb.toolName || '(unknown tool)';
          toolDiv.appendChild(toolLabel);
          block.appendChild(toolDiv);

          if (cb.input && self.#showToolInputEl && self.#showToolInputEl.checked) {
            var inputDiv = document.createElement('div');
            inputDiv.className = 'tool-input';
            var inputStr = typeof cb.input === 'string' ? cb.input : JSON.stringify(cb.input, null, 2);
            inputDiv.textContent = truncate(inputStr, 2000);
            block.appendChild(inputDiv);
          }
        } else if (cb.type === 'tool_result' || cb.output !== undefined) {
          if (cb.output && self.#showToolOutputEl && self.#showToolOutputEl.checked) {
            var outputDiv = document.createElement('div');
            outputDiv.className = 'tool-output';
            if (cb.isError) outputDiv.style.color = '#f85149';
            outputDiv.textContent = truncate(cb.output, 2000);
            block.appendChild(outputDiv);
          }
        } else if (cb.type === 'thinking' && cb.text) {
          var thinkDiv = document.createElement('div');
          thinkDiv.className = 'event-content';
          thinkDiv.style.fontStyle = 'italic';
          thinkDiv.style.color = '#a371f7';
          thinkDiv.textContent = truncate(cb.text, 1000);
          block.appendChild(thinkDiv);
        }
      });
    }

    // Token usage
    if (e.tokenUsage) {
      var usageDiv = document.createElement('div');
      usageDiv.className = 'token-usage';
      var parts = [];
      if (e.tokenUsage.inputTokens) parts.push('in:' + e.tokenUsage.inputTokens.toLocaleString());
      if (e.tokenUsage.outputTokens) parts.push('out:' + e.tokenUsage.outputTokens.toLocaleString());
      if (e.tokenUsage.cacheRead) parts.push('cache:' + e.tokenUsage.cacheRead.toLocaleString());
      usageDiv.textContent = parts.join(' | ');
      block.appendChild(usageDiv);
    }

    // Duration
    if (e.durationMs) {
      var durDiv = document.createElement('div');
      durDiv.className = 'token-usage';
      durDiv.textContent = (e.durationMs / 1000).toFixed(1) + 's';
      block.appendChild(durDiv);
    }

    return block;
  }

  // --- Scroll ---

  #scrollToBottom(instant) {
    if (!this.#autoScroll) return;
    var wrap = this.#eventsWrap;
    // Always use immediate scrolling to avoid visible animated jumps when
    // switching agents and rendering large snapshots.
    wrap.style.scrollBehavior = 'auto';
    wrap.scrollTop = wrap.scrollHeight;
  }

  // --- Attachments ---

  #hasFilesDT(dt) {
    if (!dt || !dt.types) return false;
    for (var i = 0; i < dt.types.length; i++) {
      if (dt.types[i] === 'Files') return true;
    }
    return false;
  }

  #queueFiles(files) {
    if (!files) return;
    for (var i = 0; i < files.length; i++) {
      var file = files[i];
      if (!file) continue;
      if (file.size > MAX_UPLOAD_BYTES) {
        console.warn('skipping oversized file', file.name, file.size);
        continue;
      }
      var url = file.type.startsWith('image/') ? URL.createObjectURL(file) : null;
      this.#attachments.push({ id: ++this.#nextAttachId, file: file, url: url });
    }
    this.#renderAttachments();
  }

  #removeAttachment(id) {
    var idx = this.#attachments.findIndex(function(a) { return a.id === id; });
    if (idx < 0) return;
    if (this.#attachments[idx].url) URL.revokeObjectURL(this.#attachments[idx].url);
    this.#attachments.splice(idx, 1);
    this.#renderAttachments();
  }

  #renderAttachments() {
    clearChildren(this.#attachmentsEl);
    this.#attachmentsEl.style.display = this.#attachments.length ? 'flex' : 'none';

    var self = this;
    this.#attachments.forEach(function(att) {
      var thumb = document.createElement('div');
      thumb.className = 'attachment-thumb';
      thumb.addEventListener('click', function(ev) {
        if (ev.target.classList.contains('attachment-remove')) return;
        self.#showLightbox(att);
      });

      if (att.url) {
        var img = document.createElement('img');
        img.src = att.url;
        img.alt = att.file.name;
        thumb.appendChild(img);
      } else {
        var icon = document.createElement('div');
        icon.className = 'file-icon';
        icon.textContent = att.file.name;
        thumb.appendChild(icon);
      }

      var removeBtn = document.createElement('button');
      removeBtn.className = 'attachment-remove';
      removeBtn.textContent = '\u00d7';
      removeBtn.title = 'Remove';
      removeBtn.addEventListener('click', function(ev) {
        ev.stopPropagation();
        self.#removeAttachment(att.id);
      });
      thumb.appendChild(removeBtn);

      self.#attachmentsEl.appendChild(thumb);
    });
  }

  // --- Lightbox ---

  #showLightbox(att) {
    var overlay = document.createElement('div');
    overlay.className = 'lightbox';
    function dismiss() { overlay.remove(); document.removeEventListener('keydown', onKey); }
    function onKey(ev) { if (ev.key === 'Escape') dismiss(); }
    overlay.addEventListener('click', dismiss);
    document.addEventListener('keydown', onKey);

    if (att.url) {
      var img = document.createElement('img');
      img.src = att.url;
      img.alt = att.file.name;
      overlay.appendChild(img);
    } else {
      var box = document.createElement('div');
      box.className = 'file-preview';
      var fname = document.createElement('div');
      fname.className = 'filename';
      fname.textContent = att.file.name;
      box.appendChild(fname);
      var fsize = document.createElement('div');
      fsize.className = 'filesize';
      fsize.textContent = formatFileSize(att.file.size) + ' \u2022 ' + (att.file.type || 'unknown type');
      box.appendChild(fsize);
      overlay.appendChild(box);
    }

    document.body.appendChild(overlay);
  }

  // --- Send prompt ---

  #sendAll() {
    var text = this.#promptInput.value.trim();
    var hasText = text.length > 0;
    var hasFiles = this.#attachments.length > 0;
    if (!hasText && !hasFiles) return;

    var name = this.getAttribute('name') || '';

    // Collect files before clearing
    var files = this.#attachments.map(function(a) { return a.file; });

    // Clear UI immediately
    this.#promptInput.value = '';
    this.#promptInput.style.height = 'auto';
    this.#sendBtn.classList.add('sending');
    if (hasFiles) this.clearAttachments();

    // Dispatch event — the dashboard shell handles WebSocket transport
    this.dispatchEvent(new CustomEvent('converter-send', {
      bubbles: true,
      detail: {
        name: name,
        prompt: hasText ? text : '',
        files: files
      }
    }));

    var btn = this.#sendBtn;
    setTimeout(function() { btn.classList.remove('sending'); }, 500);
  }
}
