package ws

import "testing"

func TestTmuxKeyNameFromVT(t *testing.T) {
	cases := []struct {
		payload string
		wantKey string
	}{
		{payload: "\x1b[Z", wantKey: "BTab"},
		{payload: "\x1b[A", wantKey: "Up"},
		{payload: "\x1b[B", wantKey: "Down"},
		{payload: "\x1b[C", wantKey: "Right"},
		{payload: "\x1b[D", wantKey: "Left"},
		{payload: "\x1b[5~", wantKey: "PgUp"},
		{payload: "\x1b[6~", wantKey: "PgDn"},
		{payload: "\x1b", wantKey: "Escape"},
		{payload: "\x7f", wantKey: "BSpace"},
	}

	for _, tc := range cases {
		got, ok := tmuxKeyNameFromVT([]byte(tc.payload))
		if !ok {
			t.Fatalf("tmuxKeyNameFromVT(%q) returned ok=false", tc.payload)
		}
		if got != tc.wantKey {
			t.Fatalf("tmuxKeyNameFromVT(%q) = %q, want %q", tc.payload, got, tc.wantKey)
		}
	}
}

func TestTmuxKeyNameFromVTUnknown(t *testing.T) {
	if _, ok := tmuxKeyNameFromVT([]byte("not-a-vt-seq")); ok {
		t.Fatal("expected unknown VT sequence to return ok=false")
	}
}

func TestParseBinaryEnvelope(t *testing.T) {
	data := []byte{BinaryKeyboardInput}
	data = append(data, []byte("hq-mayor")...)
	data = append(data, 0)
	data = append(data, []byte("abc")...)

	msgType, agentName, payload, err := parseBinaryEnvelope(data)
	if err != nil {
		t.Fatalf("parseBinaryEnvelope() error = %v", err)
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
			_, _, _, err := parseBinaryEnvelope(tc.data)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
