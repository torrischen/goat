package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/common"
)

func TestListAvailableSkills(t *testing.T) {
	skillsDir := t.TempDir()

	// Create skill 1
	skillDir1 := filepath.Join(skillsDir, "skill1")
	if err := os.MkdirAll(skillDir1, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir1, common.SkillMainFile),
		[]byte("---\nname: skill1\ndescription: First skill marker\n---\n\nInstructions for skill1.\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(skill1/SKILL.md) error = %v", err)
	}

	// Create skill 2
	skillDir2 := filepath.Join(skillsDir, "skill2")
	if err := os.MkdirAll(skillDir2, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir2, common.SkillMainFile),
		[]byte("---\nname: skill2\ndescription: Second skill marker\n---\n\nInstructions for skill2.\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(skill2/SKILL.md) error = %v", err)
	}

	// Create skill 3
	skillDir3 := filepath.Join(skillsDir, "skill3")
	if err := os.MkdirAll(skillDir3, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir3, common.SkillMainFile),
		[]byte("---\nname: skill3\ndescription: Third skill marker\n---\n\nInstructions for skill3.\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(skill3/SKILL.md) error = %v", err)
	}

	actx := common.NewAgentContext(t.Context())
	actx.SetMeta(common.InternalToolSkillsDirMetaKey, skillsDir)

	result := ListAvailableSkills().Execute(actx, map[string]any{}).String()
	if !strings.Contains(result, "name: skill1\ndescription: First skill marker") {
		t.Errorf("ListAvailableSkills() result does not contain skill1 frontmatter: %s", result)
	}
	if !strings.Contains(result, "name: skill2\ndescription: Second skill marker") {
		t.Errorf("ListAvailableSkills() result does not contain skill2 frontmatter: %s", result)
	}
	if !strings.Contains(result, "name: skill3\ndescription: Third skill marker") {
		t.Errorf("ListAvailableSkills() result does not contain skill3 frontmatter: %s", result)
	}
}

func TestLoadSkillHeadersForToolRequiresOpeningFrontmatter(t *testing.T) {
	skillsDir := t.TempDir()
	validDir := filepath.Join(skillsDir, "valid")
	invalidDir := filepath.Join(skillsDir, "invalid")
	if err := os.MkdirAll(validDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(valid) error = %v", err)
	}
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(invalid) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(validDir, common.SkillMainFile),
		[]byte("---\nname: valid\ndescription: keep every frontmatter line\ncustom: value\n---\nbody\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(valid/SKILL.md) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(invalidDir, common.SkillMainFile),
		[]byte("preamble\n---\nname: invalid\ndescription: must not be listed\n---\nbody\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(invalid/SKILL.md) error = %v", err)
	}

	result := strings.Join(loadSkillHeadersForTool(skillsDir), "\n")
	if !strings.Contains(result, "name: valid\ndescription: keep every frontmatter line\ncustom: value") {
		t.Fatalf("valid frontmatter was not fully extracted: %s", result)
	}
	if strings.Contains(result, "must not be listed") {
		t.Fatalf("frontmatter after a preamble was incorrectly accepted: %s", result)
	}
}

func TestSkillToolsUseDirectoryFromAgentContext(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, common.SkillMainFile),
		[]byte("---\ndescription: Review code.\n---\n\nUse the reference.\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "reference.md"), []byte("reference content"), 0o644); err != nil {
		t.Fatalf("WriteFile(reference.md) error = %v", err)
	}

	actx := common.NewAgentContext(t.Context())
	actx.SetMeta(common.InternalToolSkillsDirMetaKey, skillsDir)

	loaded := LoadSkills().Execute(actx, map[string]any{"skills": []any{"review"}}).String()
	if !strings.Contains(loaded, "Use the reference.") {
		t.Errorf("LoadSkills() result does not contain SKILL.md content: %s", loaded)
	}
	if !strings.Contains(loaded, filepath.Join(skillDir, "reference.md")) {
		t.Errorf("LoadSkills() result does not contain custom skill path: %s", loaded)
	}

	read := ReadSpecifiedFileInSkill().Execute(actx, map[string]any{"path": "review/reference.md"}).String()
	if read != "reference content" {
		t.Errorf("ReadSpecifiedFileInSkill() = %q, want reference content", read)
	}
}

func TestReadSpecifiedFileInSkillRejectsPathOutsideDirectory(t *testing.T) {
	parent := t.TempDir()
	skillsDir := filepath.Join(parent, "skills")
	if err := os.Mkdir(skillsDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	outside := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	actx := common.NewAgentContext(t.Context())
	actx.SetMeta(common.InternalToolSkillsDirMetaKey, skillsDir)
	result := ReadSpecifiedFileInSkill().Execute(actx, map[string]any{"path": outside}).String()
	if !strings.Contains(result, "outside skills directory") {
		t.Fatalf("ReadSpecifiedFileInSkill() = %q, want path rejection", result)
	}
}
