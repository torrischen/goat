# goatc

`goatc` turns a strict YAML definition into one interactive goat Agent executable. Its provider-based tool configuration can combine locally compiled Go plugins, multiple gRPC tool-plugin services, and multiple MCP servers in the same Agent. Local `.so` files and the normalized configuration are embedded in the executable; remote providers are connected and their tools are registered at startup.

## Tool providers

Every item under `tools` selects a provider:

| Provider | Source | Result |
| --- | --- | --- |
| `go_plugin` | A Go directory or `.go` file | Compiled as a native `.so` and embedded. One entry represents one `ToolPlugin`. |
| `grpc` | A goat gRPC tool-plugin address | Connects to one remote `PluginService`. Add multiple entries to import multiple gRPC tools. |
| `mcp` | An MCP server over stdio, SSE, or Streamable HTTP | Initializes the server and imports every tool returned by `tools/list`. |
| `builtin` | `terminal` or `subagents` | Registers goat's terminal tool, optionally sandboxed with bubblewrap, or the asynchronous subagent tools. |

For compatibility, an entry with `source` and no `provider` defaults to `go_plugin`.

```yaml
tools:
  # Local Go plugins.
  - provider: go_plugin
    name: search
    source: ./tools/search
  - name: workspace # provider defaults to go_plugin
    source: ./tools/workspace

  # Multiple goat gRPC tool plugins.
  - provider: grpc
    name: translator
    address: 127.0.0.1:50051
  - provider: grpc
    name: knowledge-search
    address: 127.0.0.1:50052

  # An MCP subprocess. All tools exposed by the server are registered.
  - provider: mcp
    name: filesystem
    transport: stdio
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-filesystem"
      - /workspace
    env:
      MCP_LOG_LEVEL: info
      MCP_TOKEN: ${MCP_TOKEN}

  # A remote MCP server using Streamable HTTP.
  - provider: mcp
    name: remote-mcp
    transport: streamable_http
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}

  # Built-in tools require no plugin wrapper.
  - provider: builtin
    name: terminal
    sandbox:
      enabled: true # Linux only; requires bubblewrap.
      network: false
      writable_paths: [/workspace]
      readonly_paths: [/etc/ssl/certs]
      preserve_env: [HOME]
  - provider: builtin
    name: subagents

  # Legacy MCP SSE is also supported.
  - provider: mcp
    name: legacy-mcp
    transport: sse
    url: https://mcp.example.com/sse
```

`name` is optional for gRPC and MCP entries and is used in diagnostics; remote tool names are supplied by their servers. `${VAR}` references in MCP commands, arguments, URLs, environment values, and HTTP headers are expanded from the Agent process environment at startup. Avoid placing secrets directly in YAML because the normalized configuration is embedded in the output binary.

## Local Go plugin contract

Each `go_plugin` source must build as a Go `main` package and export the constructor expected by `agent/toolplugin`:

```go
package main

import (
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/toolplugin"
)

type Tool struct{}

func (t *Tool) Init() error { return nil }
func (t *Tool) Ping() error { return nil }
func (t *Tool) Name() string { return "echo" }
func (t *Tool) Description() string { return "Echo the supplied text." }
func (t *Tool) Parameters() common.ToolParameters {
	return common.NewToolParameters(common.ToolProperty{
		Name: "text", Type: "string", Required: true,
	})
}
func (t *Tool) Execute(_ *common.AgentContext, input map[string]any) common.ToolResult {
	return common.NewDefaultToolResult(input["text"].(string))
}

func New() toolplugin.ToolPlugin { return &Tool{} }
func main() {}
```

A configured source directory produces one `.so`. Agent and plugin builds use the same local Go toolchain and build tags. See the [tool plugin cookbook](../agent/toolplugin/README.md) for the gRPC service contract.

## Configuration

```yaml
version: v1

agent:
  name: ops-agent
  type: react # react (default) or plan_execute
  model_max_tokens_k: 128
  max_steps: 12
  enable_planning: true # ReAct planning tools; omit for plan_execute
  parallel_tools: 3
  compress: true
  skills_dir: ./skills
  special_requirements:
    - Keep answers concise.

model:
  provider: openai # openai, claude/anthropic, or gemini
  name: gpt-5
  api_key_env: OPENAI_API_KEY
  # base_url: https://example.com/v1
  # max_output_tokens: 4096

context:
  backend: file # ram, file, redis, or mongodb
  path: data/conversations # file only
  # uri: redis://localhost:6379/0 # redis or mongodb
  # database: goat # mongodb only
  # collection: context_objects # mongodb only
  # key_prefix: my-app:contextmgr: # redis or mongodb

tools:
  - provider: go_plugin
    name: search
    source: ./tools/search
  - provider: grpc
    address: 127.0.0.1:50051
  - provider: mcp
    name: filesystem
    transport: stdio
    command: npx
    args: [-y, "@modelcontextprotocol/server-filesystem", /workspace]

build:
  output: ./dist/ops-agent
  # tags: [production]

tui:
  welcome: Ask me to investigate an issue.
```

To use dependency-aware plan-and-execute orchestration, configure the Agent as follows. The planner creates and may revise a plan, while an internal ReAct executor runs each dependency-ready step with the configured tools.

```yaml
agent:
  name: ops-agent
  type: plan_execute
  model_max_tokens_k: 128
  max_steps: 8 # used as the default executor limit
  parallel_tools: 3
  compress: true
  plan:
    max_steps: 8
    executor_max_steps: 10
    max_replans: 2
```

`agent.enable_planning` is only valid for `react`: it adds planning tools inside a ReAct run. A `plan_execute` Agent uses orchestration-level planning instead. During execution, the TUI displays the generated plan, revisions, active steps, step results, tool calls, and final answer.

Source and output paths are relative to the configuration file. A non-empty `agent.skills_dir` enables skill tools and is passed to every `Agent.Do` run through `AgentDoArgs.SkillsDir`; relative skill paths are resolved from the generated executable's working directory. Skill files are runtime inputs and are not embedded in the executable. Go plugin builds are native-only because Go shared-library plugins cannot be reliably cross-compiled. A configuration containing only gRPC and MCP providers does not build or embed any `.so` files.

At startup, providers are loaded in this order: local Go plugins, gRPC services, then MCP servers. Startup fails if a configured remote provider cannot initialize or list its tools. MCP clients and stdio subprocesses are closed when the TUI exits.

## Commands

```bash
# From a Go module that depends on the same goat version as the tools:
go run github.com/torrischen/goat/goatc init -f goatc.yaml
go run github.com/torrischen/goat/goatc validate -f goatc.yaml
# Also check the API key, remote providers, bubblewrap, and configured paths.
go run github.com/torrischen/goat/goatc validate -f goatc.yaml --check-runtime
go run github.com/torrischen/goat/goatc inspect -f goatc.yaml
# Run directly; local Go plugins are compiled into temporary runtime assets.
go run github.com/torrischen/goat/goatc run -f goatc.yaml
go run github.com/torrischen/goat/goatc build -f goatc.yaml

# Override build.output:
go run github.com/torrischen/goat/goatc build -f goatc.yaml -o ./dist/agent

goatc version
OPENAI_API_KEY=... MCP_TOKEN=... ./dist/agent
```

## Release binaries

Every `v*` tag publishes prebuilt `goatc` archives and a `SHA256SUMS` file on the corresponding [GitHub Release](https://github.com/torrischen/goat/releases):

| OS | Architectures | Archive |
| --- | --- | --- |
| Linux | `amd64`, `arm64` | `.tar.gz` |
| macOS | `amd64`, `arm64` | `.tar.gz` |
| Windows | `amd64`, `arm64` | `.zip` |
| FreeBSD | `amd64`, `arm64` | `.tar.gz` |

Artifact names follow `goatc_<tag>_<os>_<arch>`. For example:

```text
goatc_v0.2.0_linux_amd64.tar.gz
goatc_v0.2.0_windows_arm64.zip
SHA256SUMS
```

Verify an individual downloaded archive from the release directory with:

```bash
grep 'goatc_v0.2.0_linux_amd64.tar.gz' SHA256SUMS | sha256sum --check
```

The Windows build can assemble Agents that use gRPC and MCP providers, but Go's native shared-library plugin mode is unavailable on Windows.

During a run:

- `Enter` submits a message. A message submitted while the Agent is working is queued with `Agent.Steer`.
- `Esc` or `Ctrl+C` cancels the active run.
- `Ctrl+C` exits when the Agent is idle.

The resulting executable is a single delivery artifact. When local Go plugins are configured, loading them requires writing the embedded `.so` files to the operating system's temporary directory at runtime.
