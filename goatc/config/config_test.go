package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
agent:
  skills_dir: " ./custom-skills "
model:
  provider: openai
  name: gpt-5
tools:
  - source: ./tools/echo
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Version != CurrentVersion {
		t.Errorf("Version = %q, want %q", cfg.Version, CurrentVersion)
	}
	if cfg.Agent.Name != "goat-agent" {
		t.Errorf("Agent.Name = %q, want goat-agent", cfg.Agent.Name)
	}
	if cfg.Agent.Type != AgentTypeReact {
		t.Errorf("Agent.Type = %q, want %q", cfg.Agent.Type, AgentTypeReact)
	}
	if cfg.Model.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("Model.APIKeyEnv = %q, want OPENAI_API_KEY", cfg.Model.APIKeyEnv)
	}
	if cfg.Agent.SkillsDir != "./custom-skills" {
		t.Errorf("Agent.SkillsDir = %q, want ./custom-skills", cfg.Agent.SkillsDir)
	}
	if cfg.Tools[0].Provider != ToolProviderGoPlugin {
		t.Errorf("Tools[0].Provider = %q, want %q", cfg.Tools[0].Provider, ToolProviderGoPlugin)
	}
	if cfg.Tools[0].Name != "echo" {
		t.Errorf("Tools[0].Name = %q, want echo", cfg.Tools[0].Name)
	}
	if cfg.Build.GOOS != runtime.GOOS || cfg.Build.GOARCH != runtime.GOARCH {
		t.Errorf("target = %s/%s, want %s/%s", cfg.Build.GOOS, cfg.Build.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
}

func TestParseRejectsRemovedContextBackends(t *testing.T) {
	for _, backend := range []string{"file", "redis"} {
		t.Run(backend, func(t *testing.T) {
			_, err := Parse([]byte("context: {backend: " + backend + "}\nmodel: {provider: openai, name: gpt-5}\ntools: [{provider: builtin, name: terminal}]\n"))
			if err == nil || !strings.Contains(err.Error(), "unsupported context.backend") {
				t.Fatalf("Parse(%q) error = %v", backend, err)
			}
		})
	}
}

func TestParseSupportsMongoDBCollections(t *testing.T) {
	cfg, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
context:
  backend: mongodb
  context_collection: context_heads
  message_collection: message_log
tools: [{provider: builtin, name: terminal}]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Context.ContextCollection != "context_heads" || cfg.Context.MessageCollection != "message_log" {
		t.Fatalf("Context collections = (%q, %q)", cfg.Context.ContextCollection, cfg.Context.MessageCollection)
	}
}

func TestParseSupportsPlanExecuteAgent(t *testing.T) {
	cfg, err := Parse([]byte(`
agent:
  type: plan-execute
  max_steps: 6
  plan:
    max_steps: 4
    executor_max_steps: 5
    max_replans: 3
model: {provider: openai, name: gpt-5}
tools: [{provider: builtin, name: terminal}]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Agent.Type != AgentTypePlanExecute {
		t.Errorf("Agent.Type = %q, want %q", cfg.Agent.Type, AgentTypePlanExecute)
	}
	if cfg.Agent.Plan == nil || cfg.Agent.Plan.MaxSteps != 4 || cfg.Agent.Plan.ExecutorMaxSteps != 5 || cfg.Agent.Plan.MaxReplans != 3 {
		t.Fatalf("Agent.Plan = %#v, want configured plan settings", cfg.Agent.Plan)
	}
}

func TestParseAppliesPlanExecuteDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
agent: {type: plan_execute, max_steps: 6}
model: {provider: openai, name: gpt-5}
tools: [{provider: builtin, name: terminal}]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Agent.Plan == nil || cfg.Agent.Plan.MaxSteps != 8 || cfg.Agent.Plan.ExecutorMaxSteps != 6 || cfg.Agent.Plan.MaxReplans != 2 {
		t.Fatalf("Agent.Plan = %#v, want defaults 8/6/2", cfg.Agent.Plan)
	}
}

func TestParseRejectsInvalidAgentTypeConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		want  string
	}{
		{name: "unknown type", agent: `{type: workflow}`, want: "unsupported agent.type"},
		{name: "plan on react", agent: `{type: react, plan: {max_steps: 4}}`, want: "agent.plan is only valid"},
		{name: "React planning on plan execute", agent: `{type: plan_execute, enable_planning: true}`, want: "agent.enable_planning is only valid"},
		{name: "negative replans", agent: `{type: plan_execute, plan: {max_replans: -1}}`, want: "max_replans cannot be negative"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte("agent: " + test.agent + "\nmodel: {provider: openai, name: gpt-5}\ntools: [{provider: builtin, name: terminal}]\n"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
model:
  provider: openai
  name: gpt-5
  unknown: true
tools:
  - source: ./echo
`))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("Parse() error = %v, want unknown field error", err)
	}
}

func TestParseRejectsDuplicateToolNames(t *testing.T) {
	_, err := Parse([]byte(`
model:
  provider: openai
  name: gpt-5
tools:
  - name: echo
    source: ./one
  - name: echo
    source: ./two
`))
	if err == nil || !strings.Contains(err.Error(), `duplicate Go plugin name "echo"`) {
		t.Fatalf("Parse() error = %v, want duplicate tool error", err)
	}
}

func TestParseSupportsToolProviders(t *testing.T) {
	cfg, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - provider: grpc
    name: translator
    address: 127.0.0.1:50051
  - provider: grpc
    address: 127.0.0.1:50052
  - provider: mcp
    name: filesystem
    transport: stdio
    command: npx
    args: [-y, "@modelcontextprotocol/server-filesystem", /tmp]
    env:
      MCP_TOKEN: ${MCP_TOKEN}
  - provider: mcp
    transport: streamable-http
    url: https://example.com/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := cfg.Tools[0].Provider; got != ToolProviderGRPC {
		t.Errorf("Tools[0].Provider = %q, want %q", got, ToolProviderGRPC)
	}
	if got := cfg.Tools[2].Transport; got != MCPTransportStdio {
		t.Errorf("Tools[2].Transport = %q, want %q", got, MCPTransportStdio)
	}
	if got := cfg.Tools[3].Transport; got != MCPTransportStreamableHTTP {
		t.Errorf("Tools[3].Transport = %q, want %q", got, MCPTransportStreamableHTTP)
	}
}

func TestParseRejectsProviderOptionsFromAnotherProvider(t *testing.T) {
	_, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - provider: grpc
    address: 127.0.0.1:50051
    source: ./unexpected
`))
	if err == nil || !strings.Contains(err.Error(), "not valid for provider") {
		t.Fatalf("Parse() error = %v, want provider option error", err)
	}
}

func TestParseRejectsDuplicateGRPCAddress(t *testing.T) {
	_, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - {provider: grpc, address: "127.0.0.1:50051"}
  - {provider: grpc, address: "127.0.0.1:50051"}
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate gRPC tool address") {
		t.Fatalf("Parse() error = %v, want duplicate gRPC address error", err)
	}
}

func TestParseRejectsMissingMCPTransport(t *testing.T) {
	_, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - provider: mcp
    url: https://example.com/mcp
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported tools[0].transport") {
		t.Fatalf("Parse() error = %v, want MCP transport error", err)
	}
}

func TestParseSupportsBuiltinTools(t *testing.T) {
	cfg, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - provider: builtin
    name: terminal
    sandbox:
      enabled: true
      writable_paths: [/workspace]
      readonly_paths: [/etc]
      preserve_env: [HOME]
  - provider: builtin
    name: subagents
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Tools[0].Sandbox == nil || !cfg.Tools[0].Sandbox.Enabled {
		t.Fatal("terminal sandbox was not parsed")
	}
	if got := cfg.Tools[1].Name; got != BuiltinSubagents {
		t.Fatalf("builtin name = %q, want %q", got, BuiltinSubagents)
	}
}

func TestParseRejectsInvalidBuiltinConfiguration(t *testing.T) {
	_, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools:
  - provider: builtin
    name: subagents
    sandbox: {enabled: true}
`))
	if err == nil || !strings.Contains(err.Error(), "sandbox is only valid") {
		t.Fatalf("Parse() error = %v, want builtin sandbox error", err)
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	_, err := Parse([]byte(`
model: {provider: openai, name: gpt-5}
tools: [{source: ./echo}]
---
model: {provider: openai, name: other}
`))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Parse() error = %v, want multiple document error", err)
	}
}
