package wsadapter

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
