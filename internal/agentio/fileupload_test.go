package agentio

import (
	"path/filepath"
	"testing"
)

func TestParseFileUploadPayload(t *testing.T) {
	payload := []byte("report.pdf\x00application/pdf\x00PDF-DATA")

	fileName, mimeType, data, err := ParseFileUploadPayload(payload)
	if err != nil {
		t.Fatalf("ParseFileUploadPayload() unexpected error: %v", err)
	}
	if fileName != "report.pdf" {
		t.Fatalf("fileName = %q, want %q", fileName, "report.pdf")
	}
	if mimeType != "application/pdf" {
		t.Fatalf("mimeType = %q, want %q", mimeType, "application/pdf")
	}
	if string(data) != "PDF-DATA" {
		t.Fatalf("data = %q, want %q", string(data), "PDF-DATA")
	}
}

func TestParseFileUploadPayloadEmptyFilenameDefaults(t *testing.T) {
	payload := []byte("\x00application/octet-stream\x00XYZ")

	fileName, _, _, err := ParseFileUploadPayload(payload)
	if err != nil {
		t.Fatalf("ParseFileUploadPayload() unexpected error: %v", err)
	}
	if fileName != "attachment.bin" {
		t.Fatalf("fileName = %q, want %q", fileName, "attachment.bin")
	}
}

func TestParseFileUploadPayloadErrors(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{name: "missing_filename_separator", payload: []byte("file-only")},
		{name: "missing_mime_separator", payload: []byte("file.txt\x00text/plain")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseFileUploadPayload(tc.payload)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestBuildServerPastePath(t *testing.T) {
	workDir := filepath.Join(string(filepath.Separator), "srv", "agent")
	inside := filepath.Join(workDir, ".tmux-adapter", "uploads", "doc.pdf")
	outside := filepath.Join(string(filepath.Separator), "tmp", "doc.pdf")

	gotInside := BuildServerPastePath(workDir, inside)
	if gotInside != ".tmux-adapter/uploads/doc.pdf" {
		t.Fatalf("inside path = %q, want %q", gotInside, ".tmux-adapter/uploads/doc.pdf")
	}

	gotOutside := BuildServerPastePath(workDir, outside)
	if gotOutside != outside {
		t.Fatalf("outside path = %q, want absolute fallback %q", gotOutside, outside)
	}

	gotNoWorkDir := BuildServerPastePath("", inside)
	if gotNoWorkDir != inside {
		t.Fatalf("empty workDir path = %q, want %q", gotNoWorkDir, inside)
	}
}

func TestBuildPastePayload(t *testing.T) {
	savedPath := "/srv/agent/.tmux-adapter/uploads/data.bin"
	pastePath := "./.tmux-adapter/uploads/data.bin"

	smallText := []byte("hello\nworld")
	gotText := BuildPastePayload(savedPath, pastePath, "text/plain", smallText)
	if string(gotText) != string(smallText) {
		t.Fatalf("small text payload should be pasted inline")
	}

	largeText := make([]byte, maxInlinePasteBytes+1)
	for i := range largeText {
		largeText[i] = 'a'
	}
	gotLarge := BuildPastePayload(savedPath, pastePath, "text/plain", largeText)
	if string(gotLarge) != pastePath {
		t.Fatalf("large text payload = %q, want %q", string(gotLarge), pastePath)
	}

	binaryData := []byte{0x00, 0x01, 0x02}
	gotBinary := BuildPastePayload(savedPath, pastePath, "application/octet-stream", binaryData)
	if string(gotBinary) != pastePath {
		t.Fatalf("binary payload = %q, want %q", string(gotBinary), pastePath)
	}

	imgData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG header bytes
	gotImg := BuildPastePayload(savedPath, pastePath, "image/png", imgData)
	if string(gotImg) != savedPath {
		t.Fatalf("image payload = %q, want absolute path %q", string(gotImg), savedPath)
	}
}

func TestSanitizePathComponent(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "", want: "attachment.bin"},
		{in: "safe-name.txt", want: "safe-name.txt"},
		{in: "hello world.txt", want: "hello_world.txt"},
		{in: ".hidden", want: "hidden"},
	}

	for _, tc := range cases {
		got := SanitizePathComponent(tc.in)
		if got != tc.want {
			t.Fatalf("SanitizePathComponent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
