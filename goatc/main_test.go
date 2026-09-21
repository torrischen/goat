package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitAndInspectCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goatc.yaml")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"init", "-f", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run(init) error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "provider: builtin") || !strings.Contains(string(data), "# Built-in terminal") {
		t.Fatalf("generated configuration is missing builtin comments:\n%s", data)
	}
	if err := run([]string{"init", "-f", path}, &stdout, &stderr); err == nil {
		t.Fatal("second run(init) error = nil, want overwrite protection")
	}

	stdout.Reset()
	if err := run([]string{"inspect", "-f", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run(inspect) error = %v", err)
	}
	output := stdout.String()
	for _, expected := range []string{"agent_type: react", "shell_command (builtin)", "normalized_config:"} {
		if !strings.Contains(output, expected) {
			t.Errorf("inspect output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestInspectReportsPlanExecuteAgent(t *testing.T) {
	cfg := `agent: {type: plan_execute, plan: {max_steps: 4}}
model: {provider: openai, name: gpt-5}
tools: [{provider: builtin, name: terminal}]
`
	path := filepath.Join(t.TempDir(), "goatc.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"inspect", "-f", path}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "agent_type: plan_execute") {
		t.Fatalf("inspect output does not report plan_execute:\n%s", stdout.String())
	}
}

func TestInspectExpandsPlanningAndSubagentTools(t *testing.T) {
	cfg := `model: {provider: openai, name: gpt-5}
agent: {enable_planning: true}
tools: [{provider: builtin, name: subagents}]
`
	path := filepath.Join(t.TempDir(), "goatc.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"inspect", "-f", path}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"generate_plan (builtin)", "update_plan (builtin)", "spawn_subagent (builtin)", "get_subagent_status (builtin)"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("inspect output does not contain %q", expected)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	originalVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = originalVersion })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if got, want := strings.TrimSpace(stdout.String()), "goatc v1.2.3"; got != want {
		t.Errorf("version output = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("version stderr = %q, want empty", stderr.String())
	}
}
