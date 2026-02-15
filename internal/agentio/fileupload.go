package agentio

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxFileUploadBytes is the maximum allowed file upload size.
const MaxFileUploadBytes = 8 * 1024 * 1024

const maxInlinePasteBytes = 256 * 1024

// HandleFileUpload stores an uploaded file server-side, copies a pasteable
// payload to the local clipboard when possible, and pastes into the tmux target.
// The caller must hold the per-agent lock.
func (p *Prompter) HandleFileUpload(agentName string, payload []byte) error {
	fileName, mimeType, fileBytes, err := ParseFileUploadPayload(payload)
	if err != nil {
		return err
	}
	if len(fileBytes) > MaxFileUploadBytes {
		return fmt.Errorf("file %q too large: %d bytes (max %d)", fileName, len(fileBytes), MaxFileUploadBytes)
	}

	agent, ok := p.Registry.GetAgent(agentName)
	if !ok {
		return fmt.Errorf("agent not found: %s", agentName)
	}

	savedPath, err := SaveUploadedFile(agent.WorkDir, agentName, fileName, fileBytes)
	if err != nil {
		return fmt.Errorf("save uploaded file: %w", err)
	}

	pasteBaseDir := agent.WorkDir
	if paneInfo, err := p.Ctrl.GetPaneInfo(agentName); err == nil && strings.TrimSpace(paneInfo.WorkDir) != "" {
		pasteBaseDir = paneInfo.WorkDir
	}
	pastePath := BuildServerPastePath(pasteBaseDir, savedPath)
	pastePayload := BuildPastePayload(savedPath, pastePath, mimeType, fileBytes)

	if err := CopyToLocalClipboard(pastePayload); err != nil {
		log.Printf("clipboard copy %s: %v", agentName, err)
	}
	if err := p.Ctrl.PasteBytes(agentName, pastePayload); err != nil {
		return fmt.Errorf("paste into tmux: %w", err)
	}

	log.Printf("file upload %s: name=%q mime=%q bytes=%d saved=%s pastePath=%s pastedBytes=%d", agentName, fileName, mimeType, len(fileBytes), savedPath, pastePath, len(pastePayload))
	return nil
}

// ParseFileUploadPayload parses the binary file upload payload.
// Format: fileName + \0 + mimeType + \0 + fileBytes
func ParseFileUploadPayload(payload []byte) (fileName string, mimeType string, data []byte, err error) {
	first := bytes.IndexByte(payload, 0)
	if first < 0 {
		return "", "", nil, fmt.Errorf("invalid file payload: missing filename separator")
	}

	secondRel := bytes.IndexByte(payload[first+1:], 0)
	if secondRel < 0 {
		return "", "", nil, fmt.Errorf("invalid file payload: missing mime separator")
	}
	second := first + 1 + secondRel

	fileName = strings.TrimSpace(string(payload[:first]))
	if fileName == "" {
		fileName = "attachment.bin"
	}
	mimeType = strings.TrimSpace(string(payload[first+1 : second]))
	data = payload[second+1:]
	return fileName, mimeType, data, nil
}

// BuildPastePayload determines what to paste into the tmux session.
func BuildPastePayload(savedPath, pastePath, mimeType string, fileBytes []byte) []byte {
	if len(fileBytes) <= maxInlinePasteBytes && IsTextLike(mimeType, fileBytes) {
		return fileBytes
	}
	// Images need the absolute path so Claude Code can read and render them inline.
	if strings.HasPrefix(mimeType, "image/") {
		return []byte(savedPath)
	}
	return []byte(pastePath)
}

// BuildServerPastePath returns a path string that is valid on the server-side
// agent machine. If the uploaded file is under the agent workdir, prefer a
// relative path so the pasted reference remains portable across hosts.
func BuildServerPastePath(workDir, savedPath string) string {
	wd := strings.TrimSpace(workDir)
	if wd == "" {
		return savedPath
	}

	rel, err := filepath.Rel(wd, savedPath)
	if err != nil || rel == "" {
		return savedPath
	}

	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return savedPath
	}
	if !strings.HasPrefix(rel, ".") {
		rel = "." + string(filepath.Separator) + rel
	}
	return filepath.ToSlash(rel)
}

// IsTextLike returns true if the content is likely human-readable text.
func IsTextLike(mimeType string, data []byte) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return IsUTF8Text(data)
	}

	switch mimeType {
	case "application/json", "application/xml", "application/x-yaml", "application/javascript":
		return IsUTF8Text(data)
	}

	return IsUTF8Text(data)
}

// IsUTF8Text returns true if data is valid UTF-8 text without control characters.
func IsUTF8Text(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}

	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	for _, b := range sample {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}

// SaveUploadedFile saves a file to disk in the agent's upload directory.
func SaveUploadedFile(workDir, agentName, fileName string, data []byte) (string, error) {
	safeName := SanitizePathComponent(fileName)
	stampedName := fmt.Sprintf("%d-%s", time.Now().UnixNano(), safeName)

	candidates := make([]string, 0, 2)
	if strings.TrimSpace(workDir) != "" {
		candidates = append(candidates, filepath.Join(workDir, ".tmux-adapter", "uploads"))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "tmux-adapter", "uploads", SanitizePathComponent(agentName)))

	var lastErr error
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}

		path := filepath.Join(dir, stampedName)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			lastErr = err
			continue
		}
		return path, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no upload path available")
	}
	return "", lastErr
}

// SanitizePathComponent makes a filename safe for use in paths.
func SanitizePathComponent(s string) string {
	base := filepath.Base(strings.TrimSpace(s))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "attachment.bin"
	}

	var b strings.Builder
	for _, r := range base {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	if out == "" {
		return "attachment.bin"
	}
	return out
}

// CopyToLocalClipboard copies data to the system clipboard.
func CopyToLocalClipboard(data []byte) error {
	commands := [][]string{
		{"pbcopy"},
		{"wl-copy"},
		{"xclip", "-selection", "clipboard", "-in"},
		{"xsel", "--clipboard", "--input"},
	}

	found := false
	var lastErr error
	for _, args := range commands {
		path, err := exec.LookPath(args[0])
		if err != nil {
			continue
		}
		found = true

		cmd := exec.Command(path, args[1:]...)
		cmd.Stdin = bytes.NewReader(data)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				lastErr = fmt.Errorf("%s failed: %w (%s)", args[0], err, msg)
			} else {
				lastErr = fmt.Errorf("%s failed: %w", args[0], err)
			}
		}
	}

	if !found {
		return fmt.Errorf("no clipboard command found")
	}
	return lastErr
}
