// Binary message type constants for the tmux-adapter WebSocket protocol.
// These match the Go server's binary frame format:
//   msgType(1 byte) + agentName(utf8) + \0 + payload

export var BinaryMsgType = {
  TerminalOutput: 0x01,
  KeyboardInput: 0x02,
  Resize: 0x03,
  FileUpload: 0x04,
  TerminalSnapshot: 0x05
};

var encoder = new TextEncoder();
var decoder = new TextDecoder();

export function encodeBinaryFrame(type, agentName, payload) {
  var nameBytes = encoder.encode(agentName);
  var buf = new Uint8Array(1 + nameBytes.length + 1 + payload.length);
  buf[0] = type;
  buf.set(nameBytes, 1);
  buf[1 + nameBytes.length] = 0; // null separator
  buf.set(payload, 1 + nameBytes.length + 1);
  return buf;
}

export function decodeBinaryFrame(buffer) {
  var bytes = new Uint8Array(buffer);
  var msgType = bytes[0];

  // Find null terminator after agent name
  var nullIdx = 1;
  while (nullIdx < bytes.length && bytes[nullIdx] !== 0) nullIdx++;

  var agentName = decoder.decode(bytes.slice(1, nullIdx));
  var payload = bytes.slice(nullIdx + 1);

  return { msgType: msgType, agentName: agentName, payload: payload };
}
