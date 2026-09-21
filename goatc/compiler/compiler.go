// Package compiler implements the goatc build pipeline.
package compiler

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/torrischen/goat/goatc/config"
	goatcruntime "github.com/torrischen/goat/goatc/runtime"
	"gopkg.in/yaml.v3"
)

// Result describes a completed build.
type Result struct {
	Output string
}

// Run compiles local plugins into temporary assets and starts the agent directly.
func Run(ctx context.Context, configPath string, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(absoluteConfig)
	assets := fstest.MapFS{}
	normalized, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode normalized config: %w", err)
	}
	assets["goatc.yaml"] = &fstest.MapFile{Data: normalized, Mode: 0o644}
	for _, tool := range cfg.Tools {
		if tool.Provider != config.ToolProviderGoPlugin {
			continue
		}
		source := tool.Source
		if !filepath.IsAbs(source) {
			source = filepath.Join(projectDir, source)
		}
		temp, err := os.CreateTemp("", "goatc-plugin-*.so")
		if err != nil {
			return fmt.Errorf("create temporary plugin: %w", err)
		}
		pluginPath := temp.Name()
		_ = temp.Close()
		defer os.Remove(pluginPath)
		fmt.Fprintf(log, "building plugin %s\n", tool.Name)
		if err := buildPlugin(source, pluginPath, cfg.Build.Tags, log); err != nil {
			return fmt.Errorf("build plugin %s: %w", tool.Name, err)
		}
		data, err := os.ReadFile(pluginPath)
		if err != nil {
			return fmt.Errorf("read plugin %s: %w", tool.Name, err)
		}
		assets["plugins/"+tool.Name+".so"] = &fstest.MapFile{Data: data, Mode: 0o700}
	}
	return goatcruntime.RunConfig(ctx, cfg, assets)
}

// Build compiles local plugins and assembles all configured tool providers into
// the generated agent executable.
func Build(configPath, outputOverride string, log io.Writer) (*Result, error) {
	if log == nil {
		log = io.Discard
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		return nil, err
	}

	projectDir := filepath.Dir(absoluteConfig)
	moduleRoot, err := findModuleRoot(projectDir)
	if err != nil {
		return nil, err
	}
	buildDir, err := os.MkdirTemp(moduleRoot, ".goatc-build-")
	if err != nil {
		return nil, fmt.Errorf("create build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	pluginsDir := filepath.Join(buildDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create plugin directory: %w", err)
	}

	hasGoPlugins := false
	for _, tool := range cfg.Tools {
		if tool.Provider != config.ToolProviderGoPlugin {
			continue
		}
		hasGoPlugins = true
		source := tool.Source
		if !filepath.IsAbs(source) {
			source = filepath.Join(projectDir, source)
		}
		source, err = filepath.Abs(source)
		if err != nil {
			return nil, fmt.Errorf("resolve source for %s: %w", tool.Name, err)
		}
		output := filepath.Join(pluginsDir, tool.Name+".so")
		fmt.Fprintf(log, "building plugin %s\n", tool.Name)
		if err := buildPlugin(source, output, cfg.Build.Tags, log); err != nil {
			return nil, fmt.Errorf("build plugin %s: %w", tool.Name, err)
		}
	}

	normalizedConfig, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode normalized config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "goatc.yaml"), normalizedConfig, 0o644); err != nil {
		return nil, fmt.Errorf("write embedded config: %w", err)
	}
	embedPattern := "goatc.yaml"
	if hasGoPlugins {
		embedPattern += " plugins/*.so"
	}
	mainSource := fmt.Sprintf(generatedMainTemplate, embedPattern)
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), []byte(mainSource), 0o644); err != nil {
		return nil, fmt.Errorf("write generated main: %w", err)
	}

	output := outputOverride
	if output == "" {
		output = cfg.Build.Output
	}
	if !filepath.IsAbs(output) {
		output = filepath.Join(projectDir, output)
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return nil, fmt.Errorf("resolve output path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	relativeBuildDir, err := filepath.Rel(moduleRoot, buildDir)
	if err != nil {
		return nil, fmt.Errorf("resolve generated package: %w", err)
	}
	args := []string{"build", "-trimpath", "-o", output}
	if len(cfg.Build.Tags) > 0 {
		args = append(args, "-tags", strings.Join(cfg.Build.Tags, ","))
	}
	args = append(args, "./"+filepath.ToSlash(relativeBuildDir))
	fmt.Fprintln(log, "building agent binary")
	if err := runGo(moduleRoot, args, log); err != nil {
		return nil, fmt.Errorf("build agent binary: %w", err)
	}

	return &Result{Output: output}, nil
}

func buildPlugin(source, output string, tags []string, log io.Writer) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	args := []string{"build", "-trimpath", "-buildmode=plugin", "-o", output}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}

	workingDir := source
	if info.IsDir() {
		args = append(args, ".")
	} else {
		if filepath.Ext(source) != ".go" {
			return fmt.Errorf("source must be a directory or .go file: %s", source)
		}
		workingDir = filepath.Dir(source)
		args = append(args, filepath.Base(source))
	}
	return runGo(workingDir, args, log)
}

func runGo(dir string, args []string, output io.Writer) error {
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Stdout = output
	command.Stderr = output
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func findModuleRoot(start string) (string, error) {
	dir := start
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", start)
		}
		dir = parent
	}
}

const generatedMainTemplate = `// Code generated by goatc. DO NOT EDIT.
package main

import (
	"context"
	"embed"
	"log"

	goatcruntime "github.com/torrischen/goat/goatc/runtime"
)

//go:embed %s
var assets embed.FS

func main() {
	if err := goatcruntime.Run(context.Background(), assets); err != nil {
		log.Fatal(err)
	}
}
`
