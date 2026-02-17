package agents

import (
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Agent represents a live AI coding agent detected in a tmux session.
type Agent struct {
	Name     string `json:"name"`     // tmux session name
	Runtime  string `json:"runtime"`  // detected runtime (claude, gemini, codex, etc.)
	WorkDir  string `json:"workDir"`  // pane working directory
	Attached bool   `json:"attached"` // session attached status
	// Internal fields used for runtime-specific discovery heuristics.
	PanePID     string `json:"-"`
	PaneCommand string `json:"-"`
}

// runtimeProcessNames maps agent runtime names to the process names they run as.
var runtimeProcessNames = map[string][]string{
	"claude":   {"node", "claude"},
	"gemini":   {"gemini"},
	"codex":    {"codex"},
	"cursor":   {"cursor-agent"},
	"auggie":   {"auggie"},
	"amp":      {"amp"},
	"opencode": {"opencode", "node", "bun"},
}

// RuntimePriority defines the order in which runtimes are checked.
// More specific runtimes first: "claude" before "opencode" because
// both list "node" as a process name.
var RuntimePriority = []string{
	"claude", "gemini", "codex", "cursor", "auggie", "amp", "opencode",
}

// knownShells is the set of process names that indicate a shell (not an agent).
var knownShells = map[string]bool{
	"bash": true, "zsh": true, "sh": true,
	"fish": true, "tcsh": true, "ksh": true,
}

// collectDescendantNamesFunc is the function used to collect descendant process
// names. Tests can replace this to avoid calling pgrep.
var collectDescendantNamesFunc = CollectDescendantNames

const maxProcessArgDetectDepth = 10

// readProcessArgsFunc/listChildPIDsFunc are replaceable in tests.
var (
	readProcessArgsFunc = readProcessArgs
	listChildPIDsFunc   = listChildPIDs
)

// IsAgentProcess checks if a pane command matches any of the expected process names.
func IsAgentProcess(command string, processNames []string) bool {
	if slices.Contains(processNames, command) {
		return true
	}
	// Codex binaries can appear as codex platform wrappers (e.g. codex-aarch64-a).
	if slices.Contains(processNames, "codex") && strings.HasPrefix(command, "codex-") {
		return true
	}
	return false
}

// IsShell checks if the command is a known shell.
func IsShell(command string) bool {
	return knownShells[command]
}

// CheckProcessBinary checks the actual binary path of a process (via ps -o comm=)
// against the expected process names. Handles the version-as-argv[0] case where
// Claude Code shows "2.1.38" as the pane command but the actual binary is "claude".
func CheckProcessBinary(pid string, processNames []string) bool {
	out, err := exec.Command("ps", "-p", pid, "-o", "comm=").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			log.Printf("CheckProcessBinary(%s): unexpected error: %v", pid, err)
		}
		return false
	}
	binaryPath := strings.TrimSpace(string(out))
	binaryName := filepath.Base(binaryPath)
	return IsAgentProcess(binaryName, processNames)
}

// DetectRuntime performs the full 3-tier agent detection across all
// known runtimes. Returns the runtime name or "" if no agent found.
func DetectRuntime(paneCommand, pid string) string {
	// Special-case node wrappers (Gemini/Claude/etc.) by inspecting process args.
	if paneCommand == "node" && pid != "" {
		if runtime := detectNodeWrappedRuntime(pid); runtime != "" {
			return runtime
		}
	}

	// Tier 1: Direct pane command match
	for _, runtime := range RuntimePriority {
		if IsAgentProcess(paneCommand, runtimeProcessNames[runtime]) {
			return runtime
		}
	}

	// Tier 2: Shell wrapping -> check descendants
	if IsShell(paneCommand) && pid != "" {
		descendants := collectDescendantNamesFunc(pid)
		for _, runtime := range RuntimePriority {
			if matchesAny(descendants, runtimeProcessNames[runtime]) {
				return runtime
			}
		}
		return "" // shell with no agent descendants
	}

	// Tier 3: Unrecognized command (e.g., version-as-argv[0] like "2.1.38")
	// Check binary path first, then descendants.
	if pid != "" {
		for _, runtime := range RuntimePriority {
			if CheckProcessBinary(pid, runtimeProcessNames[runtime]) {
				return runtime
			}
		}
		descendants := collectDescendantNamesFunc(pid)
		for _, runtime := range RuntimePriority {
			if matchesAny(descendants, runtimeProcessNames[runtime]) {
				return runtime
			}
		}
	}

	return "" // no agent found
}

// detectNodeWrappedRuntime resolves runtimes whose pane command is "node" by
// walking the process tree and inspecting argv for known CLI entrypoints.
func detectNodeWrappedRuntime(pid string) string {
	for _, args := range collectProcessTreeArgs(pid, maxProcessArgDetectDepth) {
		fields := strings.Fields(args)
		for _, token := range fields {
			base := filepath.Base(token)
			switch base {
			case "gemini":
				return "gemini"
			case "claude":
				return "claude"
			case "opencode":
				return "opencode"
			case "codex":
				return "codex"
			}
			if strings.HasPrefix(base, "codex-") {
				return "codex"
			}
		}
	}
	return ""
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

// CollectDescendantNames walks the process tree below pid, collecting all
// descendant process names. Makes O(depth) pgrep -P calls (one per tree
// level, max 10 levels).
func CollectDescendantNames(pid string) []string {
	var names []string
	collectDescendantNamesDepth(pid, &names, 0)
	return names
}

func collectDescendantNamesDepth(pid string, names *[]string, depth int) {
	if depth >= 10 {
		return
	}

	out, err := exec.Command("pgrep", "-P", pid, "-l").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			log.Printf("collectDescendantNamesDepth(%s): unexpected error: %v", pid, err)
		}
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		childPID := parts[0]
		childName := parts[1]

		*names = append(*names, childName)
		collectDescendantNamesDepth(childPID, names, depth+1)
	}
}

// matchesAny checks if any name in descendants matches any name in processNames.
func matchesAny(descendants, processNames []string) bool {
	for _, d := range descendants {
		if slices.Contains(processNames, d) {
			return true
		}
	}
	return false
}
