package conv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeWorkDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/chris/code/myproject", "-Users-chris-code-myproject"},
		{"/tmp/foo", "-tmp-foo"},
		{"/", "-"},
		{"/Users/csells/gt/hello_gastown/crew/bob", "-Users-csells-gt-hello-gastown-crew-bob"},
	}
	for _, tt := range tests {
		got := encodeWorkDir(tt.input)
		if got != tt.want {
			t.Errorf("encodeWorkDir(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClaudeDiscovererFindsFiles(t *testing.T) {
	root := t.TempDir()
	workDir := "/Users/chris/code/myproject"
	encoded := encodeWorkDir(workDir)
	projectDir := filepath.Join(root, "projects", encoded)

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a conversation file
	convPath := filepath.Join(projectDir, "abc123.jsonl")
	if err := os.WriteFile(convPath, []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subagent file
	subPath := filepath.Join(projectDir, "agent-sub1.jsonl")
	if err := os.WriteFile(subPath, []byte(`{"type":"assistant"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	disc := NewClaudeDiscoverer(root)
	result, err := disc.FindConversations("test-agent", workDir)
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}

	if len(result.WatchDirs) != 1 || result.WatchDirs[0] != projectDir {
		t.Fatalf("WatchDirs = %v, want [%s]", result.WatchDirs, projectDir)
	}

	if len(result.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(result.Files))
	}

	// Verify non-subagent file
	var mainFile, subFile *ConversationFile
	for i := range result.Files {
		if !result.Files[i].IsSubagent {
			mainFile = &result.Files[i]
		} else {
			subFile = &result.Files[i]
		}
	}

	if mainFile == nil {
		t.Fatal("no main conversation file found")
	}
	if mainFile.ConversationID != "claude:test-agent:abc123" {
		t.Fatalf("ConversationID = %q, want %q", mainFile.ConversationID, "claude:test-agent:abc123")
	}
	if mainFile.Runtime != "claude" {
		t.Fatalf("Runtime = %q, want %q", mainFile.Runtime, "claude")
	}

	if subFile == nil {
		t.Fatal("no subagent file found")
	}
	if !subFile.IsSubagent {
		t.Fatal("subagent file should have IsSubagent=true")
	}
}

func TestClaudeDiscovererMissingDir(t *testing.T) {
	root := t.TempDir()

	disc := NewClaudeDiscoverer(root)
	result, err := disc.FindConversations("test-agent", "/nonexistent/path")
	if err != nil {
		t.Fatalf("FindConversations() error = %v (should return empty, not error)", err)
	}

	if len(result.Files) != 0 {
		t.Fatalf("got %d files, want 0 for missing directory", len(result.Files))
	}

	if len(result.WatchDirs) == 0 {
		t.Fatal("WatchDirs should be set even for missing directory")
	}
}

func TestConversationIDUniqueness(t *testing.T) {
	// Two agents with the same native file should produce different ConversationIDs
	root := t.TempDir()
	workDir1 := "/tmp/project1"
	workDir2 := "/tmp/project2"

	for _, wd := range []string{workDir1, workDir2} {
		dir := filepath.Join(root, "projects", encodeWorkDir(wd))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "same-id.jsonl"), []byte(`{"type":"user"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	disc := NewClaudeDiscoverer(root)

	r1, _ := disc.FindConversations("agent-a", workDir1)
	r2, _ := disc.FindConversations("agent-b", workDir2)

	if len(r1.Files) == 0 || len(r2.Files) == 0 {
		t.Fatal("expected files from both agents")
	}

	if r1.Files[0].ConversationID == r2.Files[0].ConversationID {
		t.Fatalf("ConversationIDs should differ: %q == %q", r1.Files[0].ConversationID, r2.Files[0].ConversationID)
	}
}
