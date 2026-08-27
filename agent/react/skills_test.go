package react

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/llm/llmtest"
)

type skillCaptureModel struct {
	mu        sync.Mutex
	inputs    [][]*message.Message
	responses []*message.Message
	calls     int
}

func (m *skillCaptureModel) ModelID() string { return "test-model" }

func (m *skillCaptureModel) Generate(
	_ context.Context,
	_ []*message.Message,
	_ ...llm.CallOption,
) (*message.Message, error) {
	return nil, errors.New("unexpected Generate call")
}

func (m *skillCaptureModel) Stream(
	_ context.Context,
	input []*message.Message,
	_ ...llm.CallOption,
) (llm.StreamReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, common.CloneAgenticMessages(input))
	response := common.AssistantTextMessage("done")
	if m.calls < len(m.responses) {
		response = m.responses[m.calls]
	}
	m.calls++
	return llmtest.NewStreamReader([]*message.Message{response}), nil
}

func (m *skillCaptureModel) systemPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) == 0 {
		return ""
	}
	return m.inputs[len(m.inputs)-1][0].PlainText()
}

func (m *skillCaptureModel) systemPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, 0, len(m.inputs))
	for _, input := range m.inputs {
		if len(input) > 0 {
			result = append(result, input[0].PlainText())
		}
	}
	return result
}

func TestDoUsesPerRunSkillsDirAndContextMeta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	skillsDir := t.TempDir()
	writeTestSkill(t, skillsDir, "custom-skill", "custom skill marker")

	llm := &skillCaptureModel{responses: []*message.Message{
		skillProbeToolCall("skills-probe-1"),
		common.AssistantTextMessage("done"),
	}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	agent.EnableSkills()

	var toolSkillsDir string
	agent.AddTool(ctx, common.NewDefaultTool(
		"capture_skills_dir",
		"Capture the configured skill root for a test.",
		common.NewToolParameters(),
		func(actx *common.AgentContext, _ map[string]any) common.ToolResult {
			toolSkillsDir = common.SkillsDirFromContext(actx)
			return common.NewDefaultToolResult("captured")
		},
	))
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "use the custom skill"},
		SkillsDir: skillsDir,
		ContextMeta: map[common.AgentDoMetaKey]any{
			common.InternalToolSkillsDirMetaKey: "must-be-overridden",
		},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	events := readAllEvents(t, ctx, eventStream)
	if got := len(eventsByType[common.FinalAnswerCompletedEvent](events)); got != 1 {
		t.Fatalf("final answer event count = %d, want 1", got)
	}
	if toolSkillsDir != skillsDir {
		t.Errorf("tool skills dir = %q, want %q", toolSkillsDir, skillsDir)
	}
	// With dynamic skill loading, the system prompt should NOT contain skill markers
	// Instead, it should instruct the agent to call list_available_skills
	prompt := llm.systemPrompt()
	if !strings.Contains(prompt, "list_available_skills") {
		t.Fatalf("system prompt does not contain list_available_skills instruction:\n%s", prompt)
	}
	if strings.Contains(prompt, "custom skill marker") {
		t.Fatalf("system prompt should not contain skill markers (dynamic loading):\n%s", prompt)
	}
}

func TestDoReloadsSkillsFromEachRunDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	firstDir := t.TempDir()
	secondDir := t.TempDir()
	writeTestSkill(t, firstDir, "first", "first directory marker")
	writeTestSkill(t, secondDir, "second", "second directory marker")

	llm := &skillCaptureModel{}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	agent.EnableSkills()
	for _, skillsDir := range []string{firstDir, secondDir} {
		_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
			UserInput: common.AgentUserInput{Text: "use skills"},
			SkillsDir: skillsDir,
		})
		if err != nil {
			t.Fatalf("Do(%q) error = %v", skillsDir, err)
		}
		_ = readAllEvents(t, ctx, eventStream)
	}

	// With dynamic skill loading, system prompts should be stable across runs
	// They should not contain skill-specific markers, only the instruction to call list_available_skills
	prompts := llm.systemPrompts()
	if len(prompts) != 2 {
		t.Fatalf("model prompts = %d, want 2", len(prompts))
	}
	for i, prompt := range prompts {
		if !strings.Contains(prompt, "list_available_skills") {
			t.Errorf("prompt %d does not contain list_available_skills instruction", i)
		}
		if strings.Contains(prompt, "first directory marker") || strings.Contains(prompt, "second directory marker") {
			t.Errorf("prompt %d should not contain skill markers (dynamic loading):\n%s", i, prompt)
		}
	}
	// Both prompts should be identical since skills are loaded dynamically
	if prompts[0] != prompts[1] {
		t.Errorf("prompts should be identical with dynamic skill loading")
	}
}

func TestDoDefaultsSkillsDir(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &skillCaptureModel{responses: []*message.Message{
		skillProbeToolCall("skills-probe-2"),
		common.AssistantTextMessage("done"),
	}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	var got string
	agent.AddTool(ctx, common.NewDefaultTool(
		"capture_skills_dir",
		"Capture the configured skill root for a test.",
		common.NewToolParameters(),
		func(actx *common.AgentContext, _ map[string]any) common.ToolResult {
			got = common.SkillsDirFromContext(actx)
			return common.NewDefaultToolResult("captured")
		},
	))
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "hello"},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = readAllEvents(t, ctx, eventStream)
	if got != common.SkillDefaultFolder {
		t.Errorf("skills dir = %q, want %q", got, common.SkillDefaultFolder)
	}
}

func skillProbeToolCall(callID string) *message.Message {
	return &message.Message{
		Role: message.RoleAssistant,
		Blocks: []*message.ContentBlock{{Kind: message.BlockToolCall, ToolCall: &message.ToolCall{
			CallID: callID, Name: "capture_skills_dir", Arguments: `{}`,
		}}},
	}
}

func writeTestSkill(t *testing.T, root, name, marker string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + marker + "\n---\n\nInstructions.\n"
	if err := os.WriteFile(filepath.Join(dir, common.SkillMainFile), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
