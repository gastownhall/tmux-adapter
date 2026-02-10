package agents

import (
	"os/exec"
	"slices"
	"strings"
)

// Agent represents a live AI coding agent running in gastown.
type Agent struct {
	Name     string  `json:"name"`
	Role     string  `json:"role"`
	Runtime  string  `json:"runtime"`
	Rig      *string `json:"rig"`
	WorkDir  string  `json:"workDir"`
	Attached bool    `json:"attached"`
}

// runtimeProcessNames maps agent preset names to the process names they run as.
var runtimeProcessNames = map[string][]string{
	"claude":  {"node", "claude"},
	"gemini":  {"gemini"},
	"codex":   {"codex"},
	"cursor":  {"cursor-agent"},
	"auggie":  {"auggie"},
	"amp":     {"amp"},
	"opencode": {"opencode", "node", "bun"},
}

// knownShells is the set of process names that indicate a shell (not an agent).
var knownShells = map[string]bool{
	"bash": true, "zsh": true, "sh": true,
	"fish": true, "tcsh": true, "ksh": true,
}

// GetProcessNames returns the process names for a given agent preset.
// Falls back to claude's names if unknown.
func GetProcessNames(agentName string) []string {
	if names, ok := runtimeProcessNames[agentName]; ok {
		return names
	}
	return runtimeProcessNames["claude"]
}

// IsAgentProcess checks if a pane command matches any of the expected process names.
func IsAgentProcess(command string, processNames []string) bool {
	return slices.Contains(processNames, command)
}

// IsShell checks if the command is a known shell.
func IsShell(command string) bool {
	return knownShells[command]
}

// CheckDescendants walks the process tree looking for a matching process name.
// Max depth of 10 to prevent infinite loops.
func CheckDescendants(pid string, processNames []string) bool {
	return checkDescendantsDepth(pid, processNames, 0)
}

func checkDescendantsDepth(pid string, processNames []string, depth int) bool {
	if depth >= 10 {
		return false
	}

	out, err := exec.Command("pgrep", "-P", pid, "-l").Output()
	if err != nil {
		return false
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

		if IsAgentProcess(childName, processNames) {
			return true
		}
		if checkDescendantsDepth(childPID, processNames, depth+1) {
			return true
		}
	}
	return false
}

// ParseSessionName extracts role and rig from a gastown session name.
// Returns role and rig (empty string for town-level agents).
func ParseSessionName(name string) (role string, rig string) {
	// Town-level: hq-ROLE
	if rest, ok := strings.CutPrefix(name, "hq-"); ok {
		return rest, ""
	}

	// Rig-level: gt-RIG-ROLE or gt-RIG-crew-NAME or gt-RIG-NAME (polecat)
	if rest, ok := strings.CutPrefix(name, "gt-"); ok {

		// Special case: gt-boot
		if rest == "boot" {
			return "boot", ""
		}

		// Find the rig name — it's the first segment
		// gt-RIGNAME-witness, gt-RIGNAME-refinery, gt-RIGNAME-crew-NAME, gt-RIGNAME-NAME
		parts := strings.SplitN(rest, "-", 3)
		if len(parts) < 2 {
			return "unknown", ""
		}

		rigName := parts[0]
		remainder := parts[1]

		// Check for known rig-level roles
		switch remainder {
		case "witness":
			return "witness", rigName
		case "refinery":
			return "refinery", rigName
		case "overseer":
			return "overseer", rigName
		}

		// Check for crew: gt-RIG-crew-NAME
		if remainder == "crew" {
			return "crew", rigName
		}

		// Anything else is a polecat: gt-RIG-NAME
		return "polecat", rigName
	}

	return "unknown", ""
}

// IsGastownSession checks if a session name belongs to gastown.
func IsGastownSession(name string) bool {
	return strings.HasPrefix(name, "hq-") || strings.HasPrefix(name, "gt-")
}
