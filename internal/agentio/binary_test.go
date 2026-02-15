package agentio

import "testing"

func TestParseBinaryEnvelope(t *testing.T) {
	data := []byte{BinaryKeyboardInput}
	data = append(data, []byte("hq-mayor")...)
	data = append(data, 0)
	data = append(data, []byte("abc")...)

	msgType, agentName, payload, err := ParseBinaryEnvelope(data)
	if err != nil {
		t.Fatalf("ParseBinaryEnvelope() error = %v", err)
	}
	if msgType != BinaryKeyboardInput {
		t.Fatalf("msgType = 0x%02x, want 0x%02x", msgType, BinaryKeyboardInput)
	}
	if agentName != "hq-mayor" {
		t.Fatalf("agentName = %q, want %q", agentName, "hq-mayor")
	}
	if string(payload) != "abc" {
		t.Fatalf("payload = %q, want %q", string(payload), "abc")
	}
}

func TestParseBinaryEnvelopeErrors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{name: "too_short", data: []byte{BinaryKeyboardInput, 0}},
		{name: "missing_separator", data: []byte{BinaryKeyboardInput, 'a', 'b', 'c'}},
		{name: "missing_agent_name", data: []byte{BinaryKeyboardInput, 0, 'x'}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseBinaryEnvelope(tc.data)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestMakeBinaryFrame(t *testing.T) {
	frame := MakeBinaryFrame(BinaryTerminalOutput, "agent1", []byte("hello"))
	msgType, agentName, payload, err := ParseBinaryEnvelope(frame)
	if err != nil {
		t.Fatalf("roundtrip error: %v", err)
	}
	if msgType != BinaryTerminalOutput {
		t.Fatalf("msgType = 0x%02x, want 0x%02x", msgType, BinaryTerminalOutput)
	}
	if agentName != "agent1" {
		t.Fatalf("agentName = %q, want %q", agentName, "agent1")
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want %q", string(payload), "hello")
	}
}
