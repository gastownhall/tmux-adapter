// File transfer handlers: drag-drop and clipboard paste.
// Dispatches 'file-upload' CustomEvent with pre-framed binary payload.

var MaxUploadBytes = 8 * 1024 * 1024;
var textEncoder = new TextEncoder();

function hasFiles(dataTransfer) {
  if (!dataTransfer || !dataTransfer.types) return false;
  for (var i = 0; i < dataTransfer.types.length; i++) {
    if (dataTransfer.types[i] === 'Files') return true;
  }
  return false;
}

async function sendFiles(dispatchTarget, nameGetter, files) {
  if (!files || files.length === 0) return;

  for (var i = 0; i < files.length; i++) {
    var file = files[i];
    if (!file) continue;

    if (file.size > MaxUploadBytes) {
      console.warn('skipping oversized file', file.name, file.size);
      continue;
    }

    var fileBytes = new Uint8Array(await file.arrayBuffer());
    var nameBytes = textEncoder.encode(file.name || 'attachment.bin');
    var mimeBytes = textEncoder.encode(file.type || 'application/octet-stream');

    var payload = new Uint8Array(nameBytes.length + 1 + mimeBytes.length + 1 + fileBytes.length);
    payload.set(nameBytes, 0);
    payload[nameBytes.length] = 0;
    payload.set(mimeBytes, nameBytes.length + 1);
    payload[nameBytes.length + 1 + mimeBytes.length] = 0;
    payload.set(fileBytes, nameBytes.length + 1 + mimeBytes.length + 1);

    dispatchTarget.dispatchEvent(new CustomEvent('file-upload', {
      bubbles: true,
      detail: { name: nameGetter(), payload: payload }
    }));
  }
}

// Wire drag-drop and paste file handlers onto wrapperEl.
// Dispatches 'file-upload' events on dispatchTarget.
// nameGetter is a function returning the current agent name (handles attribute changes).
// Returns a cleanup function that removes all event listeners.
export function wireFileTransferHandlers(wrapperEl, dispatchTarget, nameGetter) {
  var dragDepth = 0;

  function onDragEnter(ev) {
    if (!hasFiles(ev.dataTransfer)) return;
    ev.preventDefault();
    dragDepth += 1;
    wrapperEl.classList.add('drag-over');
  }

  function onDragOver(ev) {
    if (!hasFiles(ev.dataTransfer)) return;
    ev.preventDefault();
    if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy';
  }

  function onDragLeave(ev) {
    ev.preventDefault();
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) wrapperEl.classList.remove('drag-over');
  }

  function onDrop(ev) {
    ev.preventDefault();
    dragDepth = 0;
    wrapperEl.classList.remove('drag-over');
    if (!hasFiles(ev.dataTransfer)) return;

    sendFiles(dispatchTarget, nameGetter, ev.dataTransfer.files).catch(function(err) {
      console.error('drop upload failed', err);
    });
  }

  function onPaste(ev) {
    var files = ev.clipboardData && ev.clipboardData.files;
    if (!files || files.length === 0) return;
    ev.preventDefault();

    sendFiles(dispatchTarget, nameGetter, files).catch(function(err) {
      console.error('paste upload failed', err);
    });
  }

  wrapperEl.addEventListener('dragenter', onDragEnter);
  wrapperEl.addEventListener('dragover', onDragOver);
  wrapperEl.addEventListener('dragleave', onDragLeave);
  wrapperEl.addEventListener('drop', onDrop);
  wrapperEl.addEventListener('paste', onPaste);

  return function cleanup() {
    wrapperEl.removeEventListener('dragenter', onDragEnter);
    wrapperEl.removeEventListener('dragover', onDragOver);
    wrapperEl.removeEventListener('dragleave', onDragLeave);
    wrapperEl.removeEventListener('drop', onDrop);
    wrapperEl.removeEventListener('paste', onPaste);
  };
}
