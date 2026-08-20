// Command goatc compiles Go tool plugins and assembles them into a standalone
// interactive goat agent binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/torrischen/goat/goatc/compiler"
	"github.com/torrischen/goat/goatc/config"
	goatcruntime "github.com/torrischen/goat/goatc/runtime"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const usage = `goatc assembles goat agents from YAML.

Usage:
  goatc init [-f goatc.yaml]
  goatc build [-f goatc.yaml] [-o output]
  goatc run [-f goatc.yaml]
  goatc validate [-f goatc.yaml] [--check-runtime]
  goatc inspect [-f goatc.yaml]
  goatc version
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "goatc: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("a command is required")
	}

	switch args[0] {
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("f", "goatc.yaml", "configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if _, err := os.Stat(*configPath); err == nil {
			return fmt.Errorf("%s already exists", *configPath)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(*configPath, []byte(initialConfig), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *configPath, err)
		}
		fmt.Fprintf(stdout, "created %s\n", *configPath)
		return nil
	case "build":
		flags := flag.NewFlagSet("build", flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("f", "goatc.yaml", "configuration file")
		output := flags.String("o", "", "output binary (overrides build.output)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		result, err := compiler.Build(*configPath, *output, stdout)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "built %s\n", result.Output)
		return nil
	case "run":
		flags := flag.NewFlagSet("run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("f", "goatc.yaml", "configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return compiler.Run(context.Background(), *configPath, stdout)
	case "validate":
		flags := flag.NewFlagSet("validate", flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("f", "goatc.yaml", "configuration file")
		checkRuntime := flags.Bool("check-runtime", false, "check credentials and runtime providers")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		if *checkRuntime {
			if err := checkLocalPaths(*configPath, cfg); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := goatcruntime.CheckRuntime(ctx, cfg); err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "%s is valid\n", *configPath)
		return nil
	case "inspect":
		flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
		flags.SetOutput(stderr)
		configPath := flags.String("f", "goatc.yaml", "configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		encoded, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent_type: %s\n", cfg.Agent.Type)
		fmt.Fprintln(stdout, "tools:")
		for _, name := range inspectToolNames(cfg) {
			fmt.Fprintf(stdout, "  - %s\n", name)
		}
		fmt.Fprintln(stdout, "normalized_config:")
		fmt.Fprint(stdout, string(encoded))
		return nil
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "goatc %s\n", version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func inspectToolNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Tools)+2)
	if cfg.Agent.EnablePlanning {
		names = append(names, "generate_plan (builtin)", "update_plan (builtin)")
	}
	for _, tool := range cfg.Tools {
		switch {
		case tool.Provider == config.ToolProviderBuiltin && tool.Name == config.BuiltinTerminal:
			names = append(names, "shell_command (builtin)")
		case tool.Provider == config.ToolProviderBuiltin && tool.Name == config.BuiltinSubagents:
			names = append(names, "spawn_subagent (builtin)", "get_subagent_status (builtin)")
		case tool.Provider == config.ToolProviderGoPlugin:
			names = append(names, tool.Name+" (go_plugin; resolved at startup)")
		default:
			label := tool.Name
			if label == "" {
				label = tool.Address
			}
			if label == "" {
				label = tool.Command
			}
			if label == "" {
				label = tool.URL
			}
			names = append(names, label+" ("+string(tool.Provider)+"; discovered at startup)")
		}
	}
	return names
}

func checkLocalPaths(configPath string, cfg *config.Config) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	base := filepath.Dir(absolute)
	for i, tool := range cfg.Tools {
		if tool.Provider != config.ToolProviderGoPlugin {
			continue
		}
		path := tool.Source
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("tools[%d]: access plugin source %q: %w", i, path, err)
		}
	}
	return nil
}

const initialConfig = `# goatc agent configuration
version: v1

agent:
  name: goat-agent
  # react (default) or plan_execute
  type: react
  max_steps: 8
  # Enable built-in planning tools for a ReAct agent.
  enable_planning: false
  # Plan-and-execute settings (uncomment together with type: plan_execute).
  # plan:
  #   max_steps: 8
  #   executor_max_steps: 8
  #   max_replans: 2

model:
  provider: openai
  name: gpt-4o
  api_key_env: OPENAI_API_KEY

context:
  backend: file
  path: data/conversations

tools:
  # Built-in terminal; commands are unsandboxed unless sandbox.enabled is true.
  - provider: builtin
    name: terminal
    # sandbox:
    #   enabled: true # Linux only; requires bubblewrap.
    #   network: false
    #   writable_paths: [/workspace]
  # Enable asynchronous subagents with these two built-in tools.
  # - provider: builtin
  #   name: subagents

build:
  output: goat-agent

tui:
  welcome: Ask me anything.
`
