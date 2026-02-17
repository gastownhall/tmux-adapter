package conv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GeminiDiscoverer finds Gemini CLI chat session files.
type GeminiDiscoverer struct {
	Root string // e.g. ~/.gemini/tmp
}

// NewGeminiDiscoverer creates a discoverer for Gemini CLI.
func NewGeminiDiscoverer(root string) *GeminiDiscoverer {
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".gemini", "tmp")
	}
	return &GeminiDiscoverer{Root: root}
}

type geminiSessionHeader struct {
	SessionID string `json:"sessionId"`
}

// FindConversations discovers Gemini chat files for the given agent/workdir.
func (d *GeminiDiscoverer) FindConversations(agentName, workDir string) (DiscoveryResult, error) {
	projectHash := geminiProjectHash(workDir)
	projectDir := filepath.Join(d.Root, projectHash)
	chatsDir := filepath.Join(projectDir, "chats")

	result := DiscoveryResult{
		WatchDirs: []string{projectDir, chatsDir},
	}

	files, err := d.scanChats(agentName, chatsDir)
	if err != nil {
		// Directory may not exist yet; return watch dirs so fsnotify can pick it up later.
		return result, nil
	}

	result.Files = files
	return result, nil
}

func (d *GeminiDiscoverer) scanChats(agentName, chatsDir string) ([]ConversationFile, error) {
	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		return nil, err
	}

	type fileWithTime struct {
		path    string
		modTime time.Time
		name    string
	}
	var candidates []fileWithTime

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, fileWithTime{
			path:    filepath.Join(chatsDir, name),
			modTime: info.ModTime(),
			name:    name,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	files := make([]ConversationFile, 0, len(candidates))
	for _, c := range candidates {
		nativeID := readGeminiSessionID(c.path)
		if nativeID == "" {
			nativeID = strings.TrimSuffix(strings.TrimPrefix(c.name, "session-"), ".json")
		}
		files = append(files, ConversationFile{
			Path:                 c.path,
			NativeConversationID: nativeID,
			ConversationID:       "gemini:" + agentName + ":" + nativeID,
			IsSubagent:           false,
			Runtime:              "gemini",
		})
	}

	return files, nil
}

func readGeminiSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var h geminiSessionHeader
	if err := json.NewDecoder(f).Decode(&h); err != nil {
		return ""
	}
	return strings.TrimSpace(h.SessionID)
}

func geminiProjectHash(workDir string) string {
	sum := sha256.Sum256([]byte(workDir))
	return hex.EncodeToString(sum[:])
}
