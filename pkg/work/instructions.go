package work

import (
	"path"
	"slices"
	"strings"
)

var instructionSet = []string{
	"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "GEMINI.md", "QWEN.md",
	".cursorrules", ".clinerules", ".goosehints", ".mcp.json",
	".claude/", ".agents/", ".openhands/", ".gemini/", ".qwen/",
	".github/copilot-instructions.md", ".github/instructions/",
}

// InstructionSet lists the agent instruction and configuration files and
// directories a harness may load from a repository, at any depth. A directory
// ends in a slash; a bare name matches a file or a directory.
func InstructionSet() []string { return slices.Clone(instructionSet) }

// IsInstructionEntry reports whether the slash-separated repository path rel
// is itself one of the InstructionSet entries.
func IsInstructionEntry(rel string) bool {
	return slices.ContainsFunc(instructionSet, func(entry string) bool {
		entry = strings.TrimSuffix(entry, "/")
		return rel == entry || strings.HasSuffix(rel, "/"+entry)
	})
}

// IsInstructionPath reports whether the slash-separated repository path rel
// is an InstructionSet entry or lies inside one.
func IsInstructionPath(rel string) bool {
	for p := path.Clean(rel); p != "." && p != "/"; p = path.Dir(p) {
		if IsInstructionEntry(p) {
			return true
		}
	}
	return false
}
