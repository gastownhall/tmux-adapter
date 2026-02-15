package agentio

import (
	"bytes"
	"fmt"
)

// Binary protocol message types.
const (
	BinaryTerminalOutput   byte = 0x01 // server → client: terminal output
	BinaryKeyboardInput    byte = 0x02 // client → server: keyboard input
	BinaryResize           byte = 0x03 // client → server: resize
	BinaryFileUpload       byte = 0x04 // client → server: file upload for paste
	BinaryTerminalSnapshot byte = 0x05 // server → client: terminal snapshot/refresh
)

// ParseBinaryEnvelope parses a binary WebSocket frame into its components.
// Format: msgType(1 byte) + agentName(utf8) + \0 + payload
func ParseBinaryEnvelope(data []byte) (msgType byte, agentName string, payload []byte, err error) {
	if len(data) < 3 {
		return 0, "", nil, fmt.Errorf("frame too short")
	}

	msgType = data[0]
	rest := data[1:]
	idx := bytes.IndexByte(rest, 0)
	if idx < 0 {
		return 0, "", nil, fmt.Errorf("missing agent separator")
	}
	if idx == 0 {
		return 0, "", nil, fmt.Errorf("missing agent name")
	}

	agentName = string(rest[:idx])
	payload = rest[idx+1:]
	return msgType, agentName, payload, nil
}

// MakeBinaryFrame builds a binary frame: msgType + agentName + \0 + payload
func MakeBinaryFrame(msgType byte, agentName string, payload []byte) []byte {
	frame := make([]byte, 0, 1+len(agentName)+1+len(payload))
	frame = append(frame, msgType)
	frame = append(frame, []byte(agentName)...)
	frame = append(frame, 0)
	frame = append(frame, payload...)
	return frame
}
