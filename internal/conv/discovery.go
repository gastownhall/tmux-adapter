package conv

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Discoverer finds conversation files for a given agent runtime.
type Discoverer interface {
	FindConversations(agentName, workDir string) (DiscoveryResult, error)
}

// DiscoveryResult holds discovered conversation files and directories to watch.
type DiscoveryResult struct {
	Files     []ConversationFile
	WatchDirs []string // fsnotify targets even when Files is empty
}

// ConversationFile describes a single conversation file on disk.
type ConversationFile struct {
	Path                 string
	NativeConversationID string // basename without extension
	ConversationID       string // "runtime:agentName:nativeId"
	IsSubagent           bool
	Runtime              string
}

// Parser converts raw conversation data into normalized events.
type Parser interface {
	Parse(raw []byte) ([]ConversationEvent, error)
	Reset()
	Runtime() string
}

// ClaudeDiscoverer finds Claude Code conversation files.
type ClaudeDiscoverer struct {
	Root string // e.g. ~/.claude
}

// NewClaudeDiscoverer creates a discoverer for Claude Code.
func NewClaudeDiscoverer(root string) *ClaudeDiscoverer {
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	return &ClaudeDiscoverer{Root: root}
}

// FindConversations discovers Claude Code conversation files for the given agent.
func (d *ClaudeDiscoverer) FindConversations(agentName, workDir string) (DiscoveryResult, error) {
	encoded := encodeWorkDir(workDir)
	projectDir := filepath.Join(d.Root, "projects", encoded)

	result := DiscoveryResult{
		WatchDirs: []string{projectDir},
	}

	files, err := d.scanDirectory(agentName, projectDir)
	if err != nil {
		// Directory may not exist yet — that's fine, return WatchDirs for monitoring
		return result, nil
	}

	result.Files = files
	return result, nil
}

func (d *ClaudeDiscoverer) scanDirectory(agentName, dir string) ([]ConversationFile, error) {
	entries, err := os.ReadDir(dir)
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, fileWithTime{
			path:    filepath.Join(dir, entry.Name()),
			modTime: info.ModTime(),
			name:    entry.Name(),
		})
	}

	// Sort by mtime descending — most recent first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	var files []ConversationFile
	for _, c := range candidates {
		stem := strings.TrimSuffix(c.name, ".jsonl")
		isSubagent := strings.HasPrefix(c.name, "agent-")
		files = append(files, ConversationFile{
			Path:                 c.path,
			NativeConversationID: stem,
			ConversationID:       "claude:" + agentName + ":" + stem,
			IsSubagent:           isSubagent,
			Runtime:              "claude",
		})
	}

	return files, nil
}

// encodeWorkDir encodes a working directory path for Claude's projects directory.
// Claude replaces '/' and '_' with '-'.
func encodeWorkDir(workDir string) string {
	s := strings.ReplaceAll(workDir, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}
