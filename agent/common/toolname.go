package common

// SanitizeToolName ensures tool names are compatible with LLM tool name rules:
// ^[a-zA-Z0-9_\\.-]+$
func SanitizeToolName(name string) string {
	if name == "" {
		return ""
	}

	b := make([]rune, 0, len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-' {
			b = append(b, r)
			continue
		}
		// Replace any invalid character with underscore.
		b = append(b, '_')
	}

	return string(b)
}

type nameWrappedTool struct {
	Tool
	name string
}

func (t *nameWrappedTool) Name() string {
	return t.name
}

// WrapToolName returns a tool wrapper with an overridden Name() if needed.
func WrapToolName(t Tool, name string) Tool {
	if t == nil || name == "" {
		return t
	}
	if t.Name() == name {
		return t
	}
	return &nameWrappedTool{
		Tool: t,
		name: name,
	}
}
