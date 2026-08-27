//go:build ignore

// Command test is a manual, end-to-end exercise of the React agent's skill
// subsystem (agent/react + agent/tools/skills.go).
//
// It writes a temporary skills tree on disk, starts a skills-enabled React
// agent with domain tools that the skills are required to call, then runs
// several scenarios and asserts observable behaviour:
//
//   - list_available_skills is called at the start of every turn
//   - load_skills loads exactly the expected skills (explicit mention,
//     implicit description match, multi-skill composition, no false positives)
//   - read_specified_file_in_skill reads the files referenced by a skill
//   - skill output contracts (markers) show up in the final answer
//   - skill instructions win over general behaviour (PII redaction)
//   - the skill file sandbox rejects paths outside the run's skills dir
//   - a per-run skills dir with no skills reports "no skills"
//
// Run it with:
//
//	OPENAI_API_KEY=sk-... go run test.go
//	OPENAI_API_KEY=sk-... go run test.go -scenario multi-skill-composition -v
//
// Exit code is non-zero when any check fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/agent/tools"
	openaiprovider "github.com/torrischen/goat/llm/provider/openai"
	"github.com/torrischen/goat/streaming"
)

const (
	triageMarker   = "TRIAGE-REPORT/v2"
	releaseMarker  = "generated with release-notes skill v3"
	redactedEmail  = "<redacted:email>"
	sensitiveEmail = "oncall@example.com"
)

var verbose bool

func main() {
	scenarioName := flag.String("scenario", "all", "scenario to run, or \"all\"")
	timeout := flag.Duration("timeout", 8*time.Minute, "total runtime budget")
	keepSkills := flag.Bool("keep-skills", false, "keep the generated skills directory for inspection")
	flag.BoolVar(&verbose, "v", false, "print every tool call and the full final answer")
	flag.Parse()

	if os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	env, err := newTestEnv(ctx)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}
	if *keepSkills {
		fmt.Printf("skills dir kept at %s\n", env.skillsDir)
	} else {
		defer env.cleanup()
	}

	selected, err := selectScenarios(*scenarioName)
	if err != nil {
		log.Fatalf("%v", err)
	}

	fmt.Printf("model      : %s\nskills dir : %s\nscenarios  : %d\n\n",
		envOr("OPENAI_MODEL", "gpt-5.2"), env.skillsDir, len(selected))

	failed := 0
	for _, sc := range selected {
		fmt.Printf("=== %s\n    %s\n", sc.name, sc.description)
		c := &checker{}
		start := time.Now()
		runErr := sc.run(ctx, env, c)
		if runErr != nil {
			c.fail("scenario error: %v", runErr)
		}
		c.report(time.Since(start))
		if !c.ok() {
			failed++
		}
		if ctx.Err() != nil {
			fmt.Println("budget exhausted, stopping")
			break
		}
	}

	fmt.Printf("\n%d/%d scenarios passed\n", len(selected)-failed, len(selected))
	if failed > 0 {
		os.Exit(1)
	}
}

func selectScenarios(name string) ([]scenario, error) {
	all := scenarios()
	if name == "all" {
		return all, nil
	}
	for _, sc := range all {
		if sc.name == name {
			return []scenario{sc}, nil
		}
	}
	names := make([]string, 0, len(all))
	for _, sc := range all {
		names = append(names, sc.name)
	}
	return nil, fmt.Errorf("unknown scenario %q; available: %s", name, strings.Join(names, ", "))
}

// testEnv owns the agent under test and the temporary skills tree.
type testEnv struct {
	agent     *react.Agent
	skillsDir string
	emptyDir  string
	outsideFn string
}

func newTestEnv(ctx context.Context) (*testEnv, error) {
	root, err := os.MkdirTemp("", "goat-skill-test-*")
	if err != nil {
		return nil, err
	}

	skillsDir := filepath.Join(root, "skills")
	emptyDir := filepath.Join(root, "skills-empty")
	for _, dir := range []string{skillsDir, emptyDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	// A file deliberately placed outside the skills root: the sandbox in
	// read_specified_file_in_skill must refuse to read it.
	outside := filepath.Join(root, "outside-secret.md")
	if err := os.WriteFile(outside, []byte("TOP-SECRET-OUTSIDE-VALUE\n"), 0o600); err != nil {
		return nil, err
	}

	if err := writeSkills(skillsDir); err != nil {
		return nil, err
	}

	opts := []openaiprovider.Option{
		openaiprovider.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, openaiprovider.WithBaseURL(baseURL))
	}
	opts = append(opts, openaiprovider.WithModel(envOr("OPENAI_MODEL", "gpt-5.2")))

	model := openaiprovider.New(opts...)

	agent := react.NewAgent(model, 128, ram.NewRAMContextManager())
	agent.EnableSkills()
	agent.AddTools(ctx,
		fetchIncidentTool(),
		serviceOwnersTool(),
		commitLogTool(),
		severityMatrixTool(),
	)

	return &testEnv{agent: agent, skillsDir: skillsDir, emptyDir: emptyDir, outsideFn: outside}, nil
}

func (e *testEnv) cleanup() {
	if e == nil || e.skillsDir == "" {
		return
	}
	_ = os.RemoveAll(filepath.Dir(e.skillsDir))
}

// writeSkills materializes a skills tree with progressive disclosure:
// SKILL.md carries the contract and points at reference files that must be
// read through read_specified_file_in_skill.
func writeSkills(root string) error {
	files := map[string]string{
		"incident-triage/SKILL.md":               skillIncidentTriage,
		"incident-triage/references/severity.md": refSeverityRules,
		"incident-triage/references/template.md": refTriageTemplate,
		"release-notes/SKILL.md":                 skillReleaseNotes,
		"release-notes/references/style.md":      refReleaseStyle,
		"pii-redaction/SKILL.md":                 skillPIIRedaction,
		"sql-migration/SKILL.md":                 skillSQLMigration,
	}

	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const skillIncidentTriage = `---
name: incident-triage
description: Triage a production incident by ID. Use when the user reports an outage, a failing service, an alert, or asks for an incident summary, severity, or owner.
---

# Incident Triage

Follow every step in order. Do not skip steps.

1. Call ` + "`fetch_incident`" + ` with the incident ID from the user request.
2. Read ` + "`incident-triage/references/severity.md`" + ` with the
   ` + "`read_specified_file_in_skill`" + ` tool and apply the severity rules there.
   Do not guess the severity from memory.
3. Call ` + "`severity_matrix`" + ` to confirm the response target for the severity
   you derived.
4. Call ` + "`service_owners`" + ` for the affected service to find the owning team.
5. Read ` + "`incident-triage/references/template.md`" + ` and format the final
   answer exactly with that template.

## Output contract

The final answer MUST start with the literal line ` + "`" + triageMarker + "`" + `.
`

const refSeverityRules = `# Severity rules (authoritative)

Apply the FIRST matching rule:

- SEV1: error_rate_pct >= 25 OR checkout/payment flow fully unavailable
- SEV2: error_rate_pct >= 5, or p95_latency_ms >= 1500 with user-visible impact
- SEV3: error_rate_pct >= 1 and no user-visible degradation
- SEV4: everything else

A degraded dependency raises the computed severity by one level
(SEV3 -> SEV2, SEV2 -> SEV1). Never lower a severity.
`

const refTriageTemplate = `# Triage template

` + triageMarker + `
Incident: <id> (<service>)
Severity: <SEVn> (rule: <the rule text you applied>)
Response target: <from severity_matrix>
Owner: <team> / <escalation contact>
Impact: <one sentence>
Evidence: <bullet list citing tool output>
Next actions: <two bullets>
`

const skillReleaseNotes = `---
name: release-notes
description: Write user-facing release notes from a commit log. Use when the user asks for release notes, a changelog, or a summary of what shipped in a version.
---

# Release Notes

1. Call ` + "`commit_log`" + ` for the requested version.
2. Read ` + "`release-notes/references/style.md`" + ` with
   ` + "`read_specified_file_in_skill`" + ` and follow the style guide strictly.
3. Group entries under ` + "`Added`" + `, ` + "`Fixed`" + `, and ` + "`Changed`" + `.
   Omit an empty group. Ignore commits marked internal.

## Output contract

End the final answer with the literal footer line:
` + releaseMarker + `
`

const refReleaseStyle = `# Style guide

- Present tense, user-facing voice. No commit hashes in the body.
- One line per change, starting with "- ".
- Never mention refactors, CI changes, or dependency bumps (internal commits).
`

const skillPIIRedaction = `---
name: pii-redaction
description: Redact personally identifiable information from any text that will be shown to the user. Use whenever output may contain email addresses, phone numbers, or personal names.
---

# PII Redaction

This skill overrides general formatting behaviour.

- Replace every email address with ` + "`" + redactedEmail + "`" + `.
- Replace every phone number with ` + "`<redacted:phone>`" + `.
- Keep team names, service names, and IDs intact.
- Apply the redaction to the final answer, not only to intermediate notes.
`

const skillSQLMigration = `---
name: sql-migration
description: Author reversible SQL schema migrations. Use only when the user asks for a database migration, DDL change, or schema rollback script.
---

# SQL Migration

Never use this skill for incident triage or release notes.
Produce an up migration and a matching down migration.
`

func fetchIncidentTool() common.Tool {
	data := map[string]string{
		"INC-4821": `{"id":"INC-4821","service":"checkout-api","error_rate_pct":7.4,` +
			`"p95_latency_ms":1830,"user_visible":true,` +
			`"dependencies":[{"name":"feature-flag-gateway","status":"degraded"},` +
			`{"name":"payment-api","status":"healthy"}],` +
			`"started_at":"2026-07-27T14:42:00Z"}`,
		"INC-4907": `{"id":"INC-4907","service":"search-api","error_rate_pct":1.2,` +
			`"p95_latency_ms":240,"user_visible":false,` +
			`"dependencies":[{"name":"index-builder","status":"healthy"}],` +
			`"started_at":"2026-07-27T09:05:00Z"}`,
	}

	return common.NewDefaultTool(
		"fetch_incident",
		"Return the read-only snapshot for one incident ID.",
		common.NewToolParameters(common.ToolProperty{
			Name: "incident_id", Type: "string", Required: true,
			Description: "Incident identifier, for example INC-4821.",
		}),
		func(_ *common.AgentContext, input map[string]any) common.ToolResult {
			id, _ := input["incident_id"].(string)
			snapshot, ok := data[strings.ToUpper(strings.TrimSpace(id))]
			if !ok {
				return common.NewDefaultToolResult(`{"error":"unknown incident"}`)
			}
			return common.NewDefaultToolResult(snapshot)
		},
	)
}

func serviceOwnersTool() common.Tool {
	// The escalation contact is an email so the pii-redaction scenario has
	// something concrete to redact.
	data := map[string]string{
		"checkout-api": `{"service":"checkout-api","team":"payments-platform",` +
			`"escalation_contact":"` + sensitiveEmail + `","pager":"+1-555-0142"}`,
		"search-api": `{"service":"search-api","team":"discovery",` +
			`"escalation_contact":"search-oncall@example.com","pager":"+1-555-0177"}`,
	}

	return common.NewDefaultTool(
		"service_owners",
		"Return the owning team and escalation contact for a service.",
		common.NewToolParameters(common.ToolProperty{
			Name: "service", Type: "string", Required: true,
			Description: "Service name, for example checkout-api.",
		}),
		func(_ *common.AgentContext, input map[string]any) common.ToolResult {
			service, _ := input["service"].(string)
			owner, ok := data[strings.ToLower(strings.TrimSpace(service))]
			if !ok {
				return common.NewDefaultToolResult(`{"error":"unknown service"}`)
			}
			return common.NewDefaultToolResult(owner)
		},
	)
}

func severityMatrixTool() common.Tool {
	targets := map[string]string{
		"SEV1": `{"severity":"SEV1","ack_minutes":5,"mitigation_minutes":30,"comms":"status page + exec update"}`,
		"SEV2": `{"severity":"SEV2","ack_minutes":15,"mitigation_minutes":120,"comms":"status page"}`,
		"SEV3": `{"severity":"SEV3","ack_minutes":60,"mitigation_minutes":480,"comms":"internal channel"}`,
		"SEV4": `{"severity":"SEV4","ack_minutes":240,"mitigation_minutes":1440,"comms":"ticket only"}`,
	}

	return common.NewDefaultTool(
		"severity_matrix",
		"Return the response target for a severity level (SEV1..SEV4).",
		common.NewToolParameters(common.ToolProperty{
			Name: "severity", Type: "string", Required: true,
			Description: "Severity label such as SEV2.",
		}),
		func(_ *common.AgentContext, input map[string]any) common.ToolResult {
			sev, _ := input["severity"].(string)
			target, ok := targets[strings.ToUpper(strings.TrimSpace(sev))]
			if !ok {
				return common.NewDefaultToolResult(`{"error":"unknown severity"}`)
			}
			return common.NewDefaultToolResult(target)
		},
	)
}

func commitLogTool() common.Tool {
	data := map[string]string{
		"v2.4.0": `{"version":"v2.4.0","commits":[` +
			`{"hash":"a1b2c3d","type":"feat","internal":false,"subject":"saved payment methods at checkout"},` +
			`{"hash":"b2c3d4e","type":"fix","internal":false,"subject":"cart total ignored regional tax"},` +
			`{"hash":"c3d4e5f","type":"chore","internal":true,"subject":"bump grpc to 1.68"},` +
			`{"hash":"d4e5f6a","type":"refactor","internal":true,"subject":"split checkout handler"},` +
			`{"hash":"e5f6a7b","type":"change","internal":false,"subject":"order confirmation emails now send within 30s"}]}`,
	}

	return common.NewDefaultTool(
		"commit_log",
		"Return the commit log for a released version.",
		common.NewToolParameters(common.ToolProperty{
			Name: "version", Type: "string", Required: true,
			Description: "Version tag, for example v2.4.0.",
		}),
		func(_ *common.AgentContext, input map[string]any) common.ToolResult {
			version, _ := input["version"].(string)
			log, ok := data[strings.ToLower(strings.TrimSpace(version))]
			if !ok {
				return common.NewDefaultToolResult(`{"error":"unknown version"}`)
			}
			return common.NewDefaultToolResult(log)
		},
	)
}

// runResult records everything a scenario needs to assert on.
type runResult struct {
	mu sync.Mutex

	signature       common.RunSignature
	finalAnswer     string
	toolNames       []string
	toolResults     map[string][]string
	skillsLoaded    []string
	skillFiles      []string
	skillFileErrors []string
	listSkillCalls  int
	failures        []string
	usage           *common.AgentUsage
}

func (r *runResult) called(name string) int {
	count := 0
	for _, n := range r.toolNames {
		if n == name {
			count++
		}
	}
	return count
}

func (r *runResult) resultsFor(name string) []string { return r.toolResults[name] }

// doRun executes one agent turn and collects the trace.
func doRun(
	ctx context.Context,
	env *testEnv,
	args *common.AgentDoArgs,
) (*runResult, error) {
	if args.SkillsDir == "" {
		args.SkillsDir = env.skillsDir
	}
	if args.MaxStep == 0 {
		args.MaxStep = 14
	}

	res := &runResult{toolResults: map[string][]string{}}

	signature, eventStream, err := env.agent.Do(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	res.signature = signature

	for {
		event, readErr := eventStream.ReadWithContext(ctx)
		if errors.Is(readErr, streaming.ErrStreamClosed) {
			break
		}
		if readErr != nil {
			return res, fmt.Errorf("read event stream: %w", readErr)
		}

		switch typed := event.(type) {
		case common.ToolCallStartedEvent:
			res.recordCall(typed)
		case common.ToolCallCompletedEvent:
			res.recordResult(typed)
		case common.ToolCallFailedEvent:
			res.mu.Lock()
			res.failures = append(res.failures,
				fmt.Sprintf("%s[%s]: %s", typed.Name, typed.Stage, typed.Error))
			res.mu.Unlock()
			vlogf("    [fail ] %-32s %s\n", typed.Name, typed.Error)
		case common.FinalAnswerCompletedEvent:
			res.finalAnswer = typed.Answer
		case common.RunCompletedEvent:
			res.usage = typed.Usage
		case common.RunFailedEvent:
			return res, fmt.Errorf("run failed during %s: %s", typed.Operation, typed.Error)
		case common.RunCanceledEvent:
			return res, fmt.Errorf("run canceled: %s", typed.Reason)
		case common.RunInterruptedEvent:
			return res, fmt.Errorf("run interrupted: %s", typed.Reason)
		}
	}

	if verbose {
		fmt.Printf("    --- final answer ---\n%s\n    --------------------\n", indent(res.finalAnswer, "    "))
	}
	return res, nil
}

func (r *runResult) recordCall(event common.ToolCallStartedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.toolNames = append(r.toolNames, event.Name)
	switch event.Name {
	case tools.InternalToolListAvailableSkills:
		r.listSkillCalls++
	case tools.InternalToolLoadSkills:
		r.skillsLoaded = append(r.skillsLoaded, tools.DisclosedSkillsNames(event.Arguments)...)
	case tools.InternalToolReadSpecifiedFileInSkill:
		if path, ok := event.Arguments["path"].(string); ok {
			r.skillFiles = append(r.skillFiles, path)
		}
	}
	vlogf("    [start] %-32s %v\n", event.Name, event.Arguments)
}

func (r *runResult) recordResult(event common.ToolCallCompletedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.toolResults[event.Name] = append(r.toolResults[event.Name], event.Result)
	if event.Name == tools.InternalToolReadSpecifiedFileInSkill &&
		strings.HasPrefix(event.Result, "Failed to read file:") {
		r.skillFileErrors = append(r.skillFileErrors, event.Result)
	}
	vlogf("    [done ] %-32s %s\n", event.Name, abbreviate(event.Result, 160))
}

// checker accumulates assertions so one scenario reports every problem.
type checker struct {
	passed int
	errs   []string
}

func (c *checker) expect(cond bool, format string, args ...any) {
	if cond {
		c.passed++
		return
	}
	c.fail(format, args...)
}

func (c *checker) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
}

func (c *checker) ok() bool { return len(c.errs) == 0 }

func (c *checker) report(elapsed time.Duration) {
	if c.ok() {
		fmt.Printf("    PASS  %d checks in %s\n\n", c.passed, elapsed.Round(time.Millisecond))
		return
	}
	fmt.Printf("    FAIL  %d passed, %d failed in %s\n", c.passed, len(c.errs), elapsed.Round(time.Millisecond))
	for _, e := range c.errs {
		fmt.Printf("          - %s\n", e)
	}
	fmt.Println()
}

// expectSkillsLoaded asserts the exact set of skills disclosed via load_skills.
func (c *checker) expectSkillsLoaded(res *runResult, want ...string) {
	got := uniqueSorted(res.skillsLoaded)
	sort.Strings(want)
	c.expect(equalStrings(got, want), "loaded skills = %v, want %v", got, want)
}

// expectNoToolFailures allows only the failures a scenario expects on purpose.
func (c *checker) expectNoToolFailures(res *runResult) {
	c.expect(len(res.failures) == 0, "unexpected tool failures: %v", res.failures)
}

type scenario struct {
	name        string
	description string
	run         func(context.Context, *testEnv, *checker) error
}

func scenarios() []scenario {
	return []scenario{
		{
			name:        "discovery-and-progressive-disclosure",
			description: "implicit match on description; skill must load, read its reference files, and honour its output contract",
			run:         runDiscovery,
		},
		{
			name:        "explicit-skill-mention",
			description: "user names release-notes explicitly; only that skill loads and the footer marker appears",
			run:         runExplicitMention,
		},
		{
			name:        "multi-skill-composition",
			description: "two skills combined; pii-redaction must override normal output and mask the contact email",
			run:         runMultiSkill,
		},
		{
			name:        "no-false-positive-skill",
			description: "unrelated request still lists skills but must not load sql-migration or any other skill",
			run:         runNoFalsePositive,
		},
		{
			name:        "skill-file-sandbox",
			description: "a path outside the run's skills dir must be rejected by read_specified_file_in_skill",
			run:         runSandbox,
		},
		{
			name:        "per-run-empty-skills-dir",
			description: "same agent, different SkillsDir with no skills; discovery must report none and the agent must answer anyway",
			run:         runEmptySkillsDir,
		},
		{
			name:        "planning-with-skills",
			description: "planning mode plus skills; plan is registered, updated, and the triage contract still holds",
			run:         runPlanningWithSkills,
		},
	}
}

func runDiscovery(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Checkout is throwing errors for EU customers. " +
			"Incident INC-4821 was opened. Tell me how bad it is and who owns it."},
		SkillUsageInstruction: "Prefer the most specific skill for the request and follow its steps in order.",
	})
	if err != nil {
		return err
	}

	c.expect(res.listSkillCalls >= 1, "list_available_skills calls = %d, want >= 1", res.listSkillCalls)
	c.expectSkillsLoaded(res, "incident-triage")
	c.expect(res.called("fetch_incident") >= 1, "fetch_incident was not called")
	c.expect(res.called("service_owners") >= 1, "service_owners was not called")
	c.expect(res.called("severity_matrix") >= 1, "severity_matrix was not called")
	c.expect(readAnySkillFile(res, "severity.md"),
		"severity.md was never read through read_specified_file_in_skill (paths: %v)", res.skillFiles)
	c.expect(readAnySkillFile(res, "template.md"),
		"template.md was never read through read_specified_file_in_skill (paths: %v)", res.skillFiles)
	c.expect(strings.Contains(res.finalAnswer, triageMarker),
		"final answer is missing the triage marker %q", triageMarker)
	// error_rate 7.4 => SEV2, raised to SEV1 by the degraded feature-flag-gateway.
	c.expect(strings.Contains(res.finalAnswer, "SEV1"),
		"final answer should derive SEV1 (SEV2 raised by a degraded dependency), got:\n%s",
		abbreviate(res.finalAnswer, 400))
	c.expect(strings.Contains(res.finalAnswer, "payments-platform"),
		"final answer should name the owning team payments-platform")
	c.expectNoToolFailures(res)
	return nil
}

func runExplicitMention(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{
			Text: "Use the release-notes skill to write the notes for v2.4.0.",
		},
	})
	if err != nil {
		return err
	}

	c.expect(res.listSkillCalls >= 1, "list_available_skills calls = %d, want >= 1", res.listSkillCalls)
	c.expectSkillsLoaded(res, "release-notes")
	c.expect(res.called("commit_log") >= 1, "commit_log was not called")
	c.expect(readAnySkillFile(res, "style.md"),
		"style.md was never read (paths: %v)", res.skillFiles)
	c.expect(strings.Contains(res.finalAnswer, releaseMarker),
		"final answer is missing the release footer %q", releaseMarker)
	// The style guide forbids internal commits and hashes in the body.
	c.expect(!strings.Contains(strings.ToLower(res.finalAnswer), "grpc"),
		"final answer leaked the internal dependency bump commit")
	c.expect(!strings.Contains(res.finalAnswer, "d4e5f6a"),
		"final answer leaked a commit hash")
	c.expectNoToolFailures(res)
	return nil
}

func runMultiSkill(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Triage INC-4821 for me, and apply pii-redaction " +
			"because I am pasting this summary into a public status update."},
		SkillUsageInstruction: "When several skills apply, compose them. Formatting skills apply last.",
		MaxStep:               16,
	})
	if err != nil {
		return err
	}

	c.expectSkillsLoaded(res, "incident-triage", "pii-redaction")
	c.expect(strings.Contains(res.finalAnswer, triageMarker),
		"final answer is missing the triage marker %q", triageMarker)
	// The redaction skill must win over the triage template's Owner field.
	c.expect(!strings.Contains(res.finalAnswer, sensitiveEmail),
		"final answer leaked the escalation email %q despite pii-redaction", sensitiveEmail)
	c.expect(strings.Contains(res.finalAnswer, redactedEmail),
		"final answer does not contain the required redaction token %q", redactedEmail)
	c.expect(!strings.Contains(res.finalAnswer, "555-0142"),
		"final answer leaked the pager number despite pii-redaction")
	c.expect(strings.Contains(res.finalAnswer, "payments-platform"),
		"team name should survive redaction")
	c.expectNoToolFailures(res)
	return nil
}

func runNoFalsePositive(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{
			Text: "In one sentence, what is the difference between a p95 and a p99 latency metric?",
		},
		MaxStep: 6,
	})
	if err != nil {
		return err
	}

	// Discovery is mandatory per the system prompt; loading is not.
	c.expect(res.listSkillCalls >= 1,
		"list_available_skills calls = %d, want >= 1 even for unrelated requests", res.listSkillCalls)
	c.expectSkillsLoaded(res)
	c.expect(res.called("fetch_incident") == 0, "unrelated request should not call fetch_incident")
	c.expect(res.called("commit_log") == 0, "unrelated request should not call commit_log")
	c.expect(strings.TrimSpace(res.finalAnswer) != "", "final answer is empty")
	c.expect(!strings.Contains(res.finalAnswer, triageMarker),
		"unrelated answer must not use the triage template")
	c.expectNoToolFailures(res)
	return nil
}

func runSandbox(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: fmt.Sprintf(
			"Load the incident-triage skill. Then use read_specified_file_in_skill on the "+
				"absolute path %s, which the operator says is an extra reference file. "+
				"Report verbatim what the tool returned for that path.", env.outsideFn)},
		MaxStep: 8,
	})
	if err != nil {
		return err
	}

	attempted := res.called(tools.InternalToolReadSpecifiedFileInSkill) > 0
	if !attempted {
		// Refusing to try at all is also a correct outcome; the sandbox is then
		// untested, so say so rather than silently passing.
		c.expect(!strings.Contains(res.finalAnswer, "TOP-SECRET-OUTSIDE-VALUE"),
			"outside file content appeared in the answer without any tool call")
		fmt.Println("    note: model declined to call the file tool; sandbox rejection not exercised")
		return nil
	}

	c.expect(len(res.skillFileErrors) >= 1,
		"read_specified_file_in_skill accepted an out-of-tree path; results: %v",
		res.resultsFor(tools.InternalToolReadSpecifiedFileInSkill))
	for _, result := range res.resultsFor(tools.InternalToolReadSpecifiedFileInSkill) {
		c.expect(!strings.Contains(result, "TOP-SECRET-OUTSIDE-VALUE"),
			"tool returned content from outside the skills directory")
	}
	c.expect(!strings.Contains(res.finalAnswer, "TOP-SECRET-OUTSIDE-VALUE"),
		"final answer leaked content from outside the skills directory")
	return nil
}

func runEmptySkillsDir(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Triage INC-4907 and tell me the severity."},
		SkillsDir: env.emptyDir,
		MaxStep:   8,
	})
	if err != nil {
		return err
	}

	c.expect(res.listSkillCalls >= 1, "list_available_skills calls = %d, want >= 1", res.listSkillCalls)
	listResults := res.resultsFor(tools.InternalToolListAvailableSkills)
	sawNone := false
	for _, result := range listResults {
		if strings.Contains(result, "No skills are currently available.") {
			sawNone = true
		}
		c.expect(!strings.Contains(result, "incident-triage"),
			"skills from the default dir leaked into a run scoped to %s", env.emptyDir)
	}
	c.expect(sawNone, "list_available_skills did not report an empty skills dir; results: %v", listResults)
	c.expectSkillsLoaded(res)
	c.expect(strings.TrimSpace(res.finalAnswer) != "", "agent produced no answer without skills")
	c.expect(!strings.Contains(res.finalAnswer, triageMarker),
		"triage template must be unavailable when the skill is not in the run's skills dir")
	return nil
}

func runPlanningWithSkills(ctx context.Context, env *testEnv, c *checker) error {
	res, err := doRun(ctx, env, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Triage INC-4821 end to end, then write the v2.4.0 " +
			"release notes. Do both in this turn."},
		EnablePlanning: true,
		PlanUsageInstruction: "Plan the two deliverables as separate milestones and " +
			"update each step as you finish it.",
		ToolExecutionOptions: &common.ToolExecutionOptions{EnableParallel: true, MaxConcurrency: 3},
		MaxStep:              20,
		SpecialRequirements:  []string{"Keep both deliverables in a single answer, clearly separated."},
	})
	if err != nil {
		return err
	}

	c.expect(res.called(tools.InternalToolGeneratePlan) >= 1, "generate_plan was not called in planning mode")
	c.expect(res.called(tools.InternalToolUpdatePlan) >= 1, "update_plan was never called")
	c.expectSkillsLoaded(res, "incident-triage", "release-notes")
	c.expect(strings.Contains(res.finalAnswer, triageMarker),
		"final answer is missing the triage marker %q", triageMarker)
	c.expect(strings.Contains(res.finalAnswer, releaseMarker),
		"final answer is missing the release footer %q", releaseMarker)
	c.expectNoToolFailures(res)
	return nil
}

func readAnySkillFile(res *runResult, suffix string) bool {
	for _, path := range res.skillFiles {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func abbreviate(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) <= limit {
		return flat
	}
	return flat[:limit] + "..."
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func vlogf(format string, args ...any) {
	if verbose {
		fmt.Printf(format, args...)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
