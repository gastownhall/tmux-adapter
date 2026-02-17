package conv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodexDiscoverer finds Codex session files under ~/.codex/sessions.
type CodexDiscoverer struct {
	Root string // e.g. ~/.codex/sessions
}

// NewCodexDiscoverer creates a discoverer for Codex.
func NewCodexDiscoverer(root string) *CodexDiscoverer {
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
	}
	return &CodexDiscoverer{Root: root}
}

type codexSessionMetaLine struct {
	Type    string `json:"type"`
	Payload struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"payload"`
}

// FindConversations discovers Codex session files for the given workdir.
func (d *CodexDiscoverer) FindConversations(agentName, workDir string) (DiscoveryResult, error) {
	result := DiscoveryResult{
		WatchDirs: defaultCodexWatchDirs(d.Root, time.Now()),
	}

	files, watchDirs, err := d.scanSessions(agentName, workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}

	result.Files = files
	result.WatchDirs = appendUniqueSortedDirs(result.WatchDirs, watchDirs...)
	return result, nil
}

func (d *CodexDiscoverer) scanSessions(agentName, workDir string) ([]ConversationFile, []string, error) {
	type candidate struct {
		file    ConversationFile
		modTime time.Time
	}

	var candidates []candidate
	watchSet := make(map[string]bool)

	err := filepath.WalkDir(d.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		meta, ok := readCodexSessionMeta(path)
		if !ok {
			return nil
		}
		if meta.Payload.CWD != workDir {
			return nil
		}

		nativeID := strings.TrimSpace(meta.Payload.ID)
		if nativeID == "" {
			nativeID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}

		candidates = append(candidates, candidate{
			file: ConversationFile{
				Path:                 path,
				NativeConversationID: nativeID,
				ConversationID:       "codex:" + agentName + ":" + nativeID,
				IsSubagent:           false,
				Runtime:              "codex",
			},
			modTime: info.ModTime(),
		})
		watchSet[filepath.Dir(path)] = true
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	files := make([]ConversationFile, 0, len(candidates))
	for _, c := range candidates {
		files = append(files, c.file)
	}

	watchDirs := make([]string, 0, len(watchSet))
	for dir := range watchSet {
		watchDirs = append(watchDirs, dir)
	}
	sort.Strings(watchDirs)

	return files, watchDirs, nil
}

func readCodexSessionMeta(path string) (codexSessionMetaLine, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexSessionMetaLine{}, false
	}
	defer f.Close()

	var line codexSessionMetaLine
	dec := json.NewDecoder(f)
	if err := dec.Decode(&line); err != nil {
		return codexSessionMetaLine{}, false
	}
	if line.Type != "session_meta" {
		return codexSessionMetaLine{}, false
	}
	return line, true
}

func defaultCodexWatchDirs(root string, now time.Time) []string {
	var dirs []string
	for _, dayOffset := range []int{-1, 0, 1} {
		t := now.AddDate(0, 0, dayOffset)
		dirs = append(dirs, filepath.Join(root, t.Format("2006"), t.Format("01"), t.Format("02")))
	}
	return appendUniqueSortedDirs(dirs)
}

func appendUniqueSortedDirs(base []string, extra ...string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	var merged []string

	for _, d := range append(base, extra...) {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		merged = append(merged, d)
	}

	sort.Strings(merged)
	return merged
}
