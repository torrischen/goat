package common

import "strings"

const (
	SkillDefaultFolder = "skills"
	SkillMainFile      = "SKILL.md"

	// InternalToolSkillsDirMetaKey stores the current run's skill root in an
	// AgentContext so every skill tool uses the same configured directory.
	InternalToolSkillsDirMetaKey AgentDoMetaKey = "skills_dir"
)

// SkillsDirFromContext returns the current run's configured skill directory.
// It falls back to SkillDefaultFolder when no value is available.
func SkillsDirFromContext(ctx *AgentContext) string {
	if ctx != nil {
		if dir, ok := ctx.GetMeta(InternalToolSkillsDirMetaKey).(string); ok {
			if dir = strings.TrimSpace(dir); dir != "" {
				return dir
			}
		}
	}
	return SkillDefaultFolder
}

func ExtractSkillHeader(text string) (string, bool) {
	lines := strings.Split(text, "\n")

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			start = i
			break
		}
	}
	if start == -1 {
		return "", false
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", false
	}

	return strings.Join(lines[start+1:end], "\n"), true
}
