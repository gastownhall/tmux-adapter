package conv

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const maxProcessHintDepth = 10

var (
	readProcessArgsFunc = readProcessArgs
	listChildPIDsFunc   = listChildPIDs
	listOpenFilesFunc   = listOpenFiles
)

var codexSessionIDFromPathRE = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// resolveRuntimeSessionID tries to extract the active runtime-native session ID
// from a pane process tree (e.g. Claude --resume <id>, codex resume <id>).
func resolveRuntimeSessionID(runtime, panePID string) string {
	if panePID == "" {
		return ""
	}

	argsList := collectProcessTreeArgs(panePID, maxProcessHintDepth)
	switch runtime {
	case "claude":
		return findFlagValue(argsList, "--resume")
	case "codex":
		if id := findCodexResumeID(argsList); id != "" {
			return id
		}
		return findCodexOpenSessionID(collectProcessTreePIDs(panePID, maxProcessHintDepth))
	default:
		return ""
	}
}

func collectProcessTreeArgs(rootPID string, maxDepth int) []string {
	pids := collectProcessTreePIDs(rootPID, maxDepth)
	result := make([]string, 0, len(pids))
	for _, pid := range pids {
		if args, err := readProcessArgsFunc(pid); err == nil {
			args = strings.TrimSpace(args)
			if args != "" {
				result = append(result, args)
			}
		}
	}
	return result
}

func collectProcessTreePIDs(rootPID string, maxDepth int) []string {
	type node struct {
		pid   string
		depth int
	}

	var pids []string
	seen := make(map[string]bool)
	queue := []node{{pid: rootPID}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.pid == "" || seen[cur.pid] {
			continue
		}
		seen[cur.pid] = true
		pids = append(pids, cur.pid)

		if cur.depth >= maxDepth {
			continue
		}
		children, err := listChildPIDsFunc(cur.pid)
		if err != nil {
			continue
		}
		for _, child := range children {
			if child == "" || seen[child] {
				continue
			}
			queue = append(queue, node{pid: child, depth: cur.depth + 1})
		}
	}

	return pids
}

func readProcessArgs(pid string) (string, error) {
	out, err := exec.Command("ps", "-p", pid, "-o", "args=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func listChildPIDs(pid string) ([]string, error) {
	out, err := exec.Command("pgrep", "-P", pid).Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil // no children
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	children := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			children = append(children, line)
		}
	}
	return children, nil
}

func findFlagValue(argsList []string, flag string) string {
	prefix := flag + "="
	for _, args := range argsList {
		fields := strings.Fields(args)
		for i := 0; i < len(fields); i++ {
			token := fields[i]
			if strings.HasPrefix(token, prefix) {
				value := strings.TrimPrefix(token, prefix)
				if value != "" {
					return value
				}
			}
			if token == flag && i+1 < len(fields) {
				value := fields[i+1]
				if value != "" && !strings.HasPrefix(value, "-") {
					return value
				}
			}
		}
	}
	return ""
}

func findCodexResumeID(argsList []string) string {
	for _, args := range argsList {
		fields := strings.Fields(args)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "resume" {
				continue
			}
			value := fields[i+1]
			if value != "" && !strings.HasPrefix(value, "-") {
				return value
			}
		}
	}
	return ""
}

func findCodexOpenSessionID(pids []string) string {
	for _, pid := range pids {
		paths, err := listOpenFilesFunc(pid)
		if err != nil {
			continue
		}
		for _, path := range paths {
			if !strings.Contains(path, "/.codex/sessions/") || !strings.HasSuffix(path, ".jsonl") {
				continue
			}
			if id := extractCodexSessionIDFromPath(path); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractCodexSessionIDFromPath(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	match := codexSessionIDFromPathRE.FindStringSubmatch(name)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func listOpenFiles(pid string) ([]string, error) {
	out, err := exec.Command("lsof", "-Fn", "-p", pid).Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) < 2 || line[0] != 'n' {
			continue
		}
		path := strings.TrimSpace(line[1:])
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}
