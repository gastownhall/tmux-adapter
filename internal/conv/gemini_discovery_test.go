package conv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeGeminiSessionFile(t *testing.T, path, sessionID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := `{
  "sessionId": "` + sessionID + `",
  "projectHash": "test",
  "startTime": "2026-02-17T04:28:32.092Z",
  "lastUpdated": "2026-02-17T04:28:32.092Z",
  "messages": []
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiProjectHash(t *testing.T) {
	got := geminiProjectHash("/Users/csells/Code/gastownhall/tmux-adapter")
	want := "d18e809315fe4e031f5bfb7b18a7ec9be7c522ec9a6c6119cc8bfaf302c61a1d"
	if got != want {
		t.Fatalf("geminiProjectHash() = %q, want %q", got, want)
	}
}

func TestNewGeminiDiscoverer(t *testing.T) {
	disc := NewGeminiDiscoverer("/custom/gemini/tmp")
	if disc.Root != "/custom/gemini/tmp" {
		t.Fatalf("Root = %q, want /custom/gemini/tmp", disc.Root)
	}

	disc2 := NewGeminiDiscoverer("")
	want := filepath.Join(os.Getenv("HOME"), ".gemini", "tmp")
	if disc2.Root != want {
		t.Fatalf("Root = %q, want %q", disc2.Root, want)
	}
}

func TestGeminiDiscovererFindsSessions(t *testing.T) {
	root := t.TempDir()
	workDir := "/tmp/project-a"
	hash := geminiProjectHash(workDir)
	chats := filepath.Join(root, hash, "chats")

	oldPath := filepath.Join(chats, "session-2026-02-16T04-19-old.json")
	newPath := filepath.Join(chats, "session-2026-02-17T04-27-new.json")
	writeGeminiSessionFile(t, oldPath, "sess-old")
	writeGeminiSessionFile(t, newPath, "sess-new")

	oldTS := mustParseTime(t, "2026-02-16T04:19:00Z")
	newTS := mustParseTime(t, "2026-02-17T04:27:00Z")
	if err := os.Chtimes(oldPath, oldTS, oldTS); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTS, newTS); err != nil {
		t.Fatal(err)
	}

	disc := NewGeminiDiscoverer(root)
	result, err := disc.FindConversations("gem-1", workDir)
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}

	if len(result.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(result.Files))
	}
	if result.Files[0].NativeConversationID != "sess-new" {
		t.Fatalf("first NativeConversationID = %q, want sess-new", result.Files[0].NativeConversationID)
	}
	if result.Files[0].ConversationID != "gemini:gem-1:sess-new" {
		t.Fatalf("ConversationID = %q, want gemini:gem-1:sess-new", result.Files[0].ConversationID)
	}
	if result.Files[0].Runtime != "gemini" {
		t.Fatalf("Runtime = %q, want gemini", result.Files[0].Runtime)
	}

	projectDir := filepath.Join(root, hash)
	chatsDir := filepath.Join(projectDir, "chats")
	if len(result.WatchDirs) != 2 || result.WatchDirs[0] != projectDir || result.WatchDirs[1] != chatsDir {
		t.Fatalf("WatchDirs = %v, want [%s %s]", result.WatchDirs, projectDir, chatsDir)
	}
}

func TestGeminiDiscovererMissingProjectDir(t *testing.T) {
	root := t.TempDir()
	disc := NewGeminiDiscoverer(root)

	result, err := disc.FindConversations("gem-1", "/tmp/missing")
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("len(Files) = %d, want 0", len(result.Files))
	}
	if len(result.WatchDirs) != 2 {
		t.Fatalf("len(WatchDirs) = %d, want 2", len(result.WatchDirs))
	}
}

func TestReadGeminiSessionIDFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readGeminiSessionID(path); got != "" {
		t.Fatalf("readGeminiSessionID(%s) = %q, want empty", path, got)
	}
}

func TestGeminiDiscovererSkipsNonSessionFiles(t *testing.T) {
	root := t.TempDir()
	workDir := "/tmp/project-b"
	hash := geminiProjectHash(workDir)
	chats := filepath.Join(root, hash, "chats")
	if err := os.MkdirAll(chats, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chats, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chats, "logs.json"), []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}
	writeGeminiSessionFile(t, filepath.Join(chats, "session-abc.json"), "sess-abc")

	disc := NewGeminiDiscoverer(root)
	result, err := disc.FindConversations("gem-2", workDir)
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(result.Files))
	}
}

func TestGeminiDiscovererSortsByMtime(t *testing.T) {
	root := t.TempDir()
	workDir := "/tmp/project-c"
	hash := geminiProjectHash(workDir)
	chats := filepath.Join(root, hash, "chats")

	files := []struct {
		name string
		id   string
		ts   time.Time
	}{
		{"session-old.json", "old-id", mustParseTime(t, "2026-02-15T00:00:00Z")},
		{"session-new.json", "new-id", mustParseTime(t, "2026-02-17T00:00:00Z")},
	}
	for _, f := range files {
		path := filepath.Join(chats, f.name)
		writeGeminiSessionFile(t, path, f.id)
		if err := os.Chtimes(path, f.ts, f.ts); err != nil {
			t.Fatal(err)
		}
	}

	disc := NewGeminiDiscoverer(root)
	result, err := disc.FindConversations("gem-3", workDir)
	if err != nil {
		t.Fatalf("FindConversations() error = %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(result.Files))
	}
	if result.Files[0].NativeConversationID != "new-id" {
		t.Fatalf("first NativeConversationID = %q, want new-id", result.Files[0].NativeConversationID)
	}
}
