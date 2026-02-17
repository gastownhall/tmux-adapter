package conv

import (
	"os/exec"
	"strings"
)

const maxProcessHintDepth = 10

var (
	readProcessArgsFunc = readProcessArgs
	listChildPIDsFunc   = listChildPIDs
)

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
		return findCodexResumeID(argsList)
	default:
		return ""
	}
}

func collectProcessTreeArgs(rootPID string, maxDepth int) []string {
	type node struct {
		pid   string
		depth int
	}

	var result []string
	seen := make(map[string]bool)
	queue := []node{{pid: rootPID}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.pid == "" || seen[cur.pid] {
			continue
		}
		seen[cur.pid] = true

		if args, err := readProcessArgsFunc(cur.pid); err == nil {
			args = strings.TrimSpace(args)
			if args != "" {
				result = append(result, args)
			}
		}

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

	return result
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
