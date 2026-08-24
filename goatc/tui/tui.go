// Package tui provides the interactive terminal UI used by generated agents.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/planexecute"
	"github.com/torrischen/goat/goatc/config"
	"github.com/torrischen/goat/streaming"
)

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	agentStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	reasoningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	borderStyle    = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
)

type model struct {
	ctx            context.Context
	agent          common.Agent
	config         *config.Config
	events         chan tea.Msg
	input          textinput.Model
	viewport       viewport.Model
	transcript     strings.Builder
	contextUID     common.ContextUID
	cancel         context.CancelFunc
	running        bool
	answerOpen     bool
	answerTextOpen bool
	status         string
	width          int
	height         int
}

type runStartedMsg struct {
	signature common.RunSignature
}

type reasoningChunkMsg string
type answerChunkMsg string
type finalAnswerMsg string

type toolStartedMsg struct {
	name  string
	input map[string]any
}

type toolFinishedMsg struct {
	name   string
	result string
}

type planCreatedMsg struct {
	plan    planexecute.Plan
	revised bool
	reason  string
}

type planStepStartedMsg struct{ step planexecute.Step }
type planStepCompletedMsg struct{ result planexecute.StepResult }

type runFinishedMsg struct {
	promptTokens     int
	cachedTokens     int
	completionTokens int
}

type runErrorMsg struct{ err error }
type runInterruptedMsg struct{ reason string }
type steerResultMsg struct{ err error }

// Run starts the interactive terminal interface.
func Run(ctx context.Context, agent common.Agent, cfg *config.Config) error {
	input := textinput.New()
	input.Placeholder = "Ask the agent..."
	input.Prompt = "> "
	input.CharLimit = 16 * 1024
	input.Focus()

	m := &model{
		ctx:      ctx,
		agent:    agent,
		config:   cfg,
		events:   make(chan tea.Msg, 256),
		input:    input,
		viewport: viewport.New(80, 20),
		status:   "ready",
	}
	if cfg.TUI.Welcome != "" {
		m.appendText(cfg.TUI.Welcome + "\n\n")
	}
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd

	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.running && m.cancel != nil {
				m.cancel()
				m.status = "cancelling"
				return m, nil
			}
			return m, tea.Quit
		case "esc":
			if m.running && m.cancel != nil {
				m.cancel()
				m.status = "cancelling"
			}
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.appendText(userStyle.Render("You") + "\n" + text + "\n\n")
			if m.running {
				if m.contextUID == "" {
					m.appendText(mutedStyle.Render("Agent is starting; try again in a moment.") + "\n\n")
					return m, nil
				}
				commands = append(commands, m.steer(text))
			} else {
				m.running = true
				m.answerOpen = false
				m.answerTextOpen = false
				m.status = "thinking"
				runCtx, cancel := context.WithCancel(m.ctx)
				m.cancel = cancel
				commands = append(commands, m.startRun(runCtx, cancel, text))
			}
			return m, tea.Batch(commands...)
		}
	case runStartedMsg:
		m.contextUID = msg.signature.ContextUID
		commands = append(commands, m.waitForEvent())
	case reasoningChunkMsg:
		if !m.answerOpen {
			m.appendText(agentStyle.Render(m.config.Agent.Name) + "\n")
			m.answerOpen = true
		}
		m.appendText(reasoningStyle.Render(string(msg)))
		commands = append(commands, m.waitForEvent())
	case answerChunkMsg:
		if !m.answerOpen {
			m.appendText(agentStyle.Render(m.config.Agent.Name) + "\n")
			m.answerOpen = true
		}
		m.answerTextOpen = true
		m.appendText(string(msg))
		commands = append(commands, m.waitForEvent())
	case finalAnswerMsg:
		if !m.answerTextOpen && msg != "" {
			if !m.answerOpen {
				m.appendText(agentStyle.Render(m.config.Agent.Name) + "\n")
				m.answerOpen = true
			}
			m.answerTextOpen = true
			m.appendText(string(msg))
		}
		commands = append(commands, m.waitForEvent())
	case toolStartedMsg:
		m.closeAnswer()
		m.appendText(toolStyle.Render(fmt.Sprintf("● %s", msg.name)) + " " + mutedStyle.Render(fmt.Sprint(msg.input)) + "\n")
		m.status = "running " + msg.name
		commands = append(commands, m.waitForEvent())
	case toolFinishedMsg:
		result := strings.TrimSpace(msg.result)
		if result != "" {
			m.appendText(mutedStyle.Render(abbreviate(result, 500)) + "\n")
		}
		m.status = "thinking"
		commands = append(commands, m.waitForEvent())
	case planCreatedMsg:
		m.closeAnswer()
		heading := "Plan"
		if msg.revised {
			heading = "Revised plan"
		}
		m.appendText(toolStyle.Render(heading+": "+msg.plan.Goal) + "\n")
		for _, step := range msg.plan.Steps {
			m.appendText(mutedStyle.Render(fmt.Sprintf("  %s. %s", step.ID, step.Description)) + "\n")
		}
		if msg.reason != "" {
			m.appendText(mutedStyle.Render("  Reason: "+msg.reason) + "\n")
		}
		m.status = "plan ready"
		commands = append(commands, m.waitForEvent())
	case planStepStartedMsg:
		m.closeAnswer()
		m.appendText(toolStyle.Render(fmt.Sprintf("● Step %s", msg.step.ID)) + " " + msg.step.Description + "\n")
		m.status = "executing step " + msg.step.ID
		commands = append(commands, m.waitForEvent())
	case planStepCompletedMsg:
		result := strings.TrimSpace(msg.result.Output)
		if result != "" {
			m.appendText(mutedStyle.Render(abbreviate(result, 500)) + "\n")
		}
		m.status = "planning next step"
		commands = append(commands, m.waitForEvent())
	case runFinishedMsg:
		m.closeAnswer()
		m.running = false
		m.cancel = nil
		m.status = fmt.Sprintf("ready · tokens %d/%d/%d", msg.promptTokens, msg.cachedTokens, msg.completionTokens)
	case runErrorMsg:
		m.closeAnswer()
		m.running = false
		m.cancel = nil
		if errors.Is(msg.err, context.Canceled) {
			m.status = "cancelled"
		} else {
			m.status = "error"
			m.appendText(errorStyle.Render("Error: "+msg.err.Error()) + "\n\n")
		}
	case runInterruptedMsg:
		m.closeAnswer()
		m.running = false
		m.cancel = nil
		m.status = "interrupted"
		if msg.reason != "" {
			m.appendText(mutedStyle.Render(msg.reason) + "\n\n")
		}
	case steerResultMsg:
		if msg.err != nil {
			m.appendText(errorStyle.Render("Could not steer: "+msg.err.Error()) + "\n\n")
		} else {
			m.appendText(mutedStyle.Render("Message queued for the next turn.") + "\n\n")
		}
	}

	var command tea.Cmd
	m.input, command = m.input.Update(message)
	commands = append(commands, command)
	m.viewport, command = m.viewport.Update(message)
	commands = append(commands, command)
	return m, tea.Batch(commands...)
}

func (m *model) View() string {
	header := titleStyle.Render(m.config.Agent.Name) + "  " + mutedStyle.Render(m.config.Model.Provider+"/"+m.config.Model.Name)
	status := mutedStyle.Render(m.status + " · Enter send · Esc cancel · Ctrl+C quit")
	body := borderStyle.Width(max(1, m.width-2)).Height(max(1, m.viewport.Height)).Render(m.viewport.View())
	return header + "\n" + body + "\n" + m.input.View() + "\n" + status
}

func (m *model) startRun(runCtx context.Context, cancel context.CancelFunc, text string) tea.Cmd {
	return func() tea.Msg {
		parallel := m.config.Agent.ParallelTools
		args := &common.AgentDoArgs{
			ContextUID:          m.contextUID,
			UserInput:           common.AgentUserInput{Text: text},
			EnablePlanning:      m.config.Agent.EnablePlanning,
			Compress:            m.config.Agent.Compress,
			SkillsDir:           m.config.Agent.SkillsDir,
			SpecialRequirements: m.config.Agent.SpecialRequirements,
		}
		if m.config.Agent.Type == config.AgentTypeReact {
			args.MaxStep = m.config.Agent.MaxSteps
		}
		if parallel > 0 {
			args.ToolExecutionOptions = &common.ToolExecutionOptions{EnableParallel: true, MaxConcurrency: parallel}
		}
		signature, events, err := m.agent.Do(runCtx, args)
		if err != nil {
			cancel()
			return runErrorMsg{err: err}
		}
		go func() {
			defer cancel()
			terminalSeen := false
			for {
				event, readErr := events.ReadWithContext(m.ctx)
				if errors.Is(readErr, streaming.ErrStreamClosed) {
					if !terminalSeen {
						m.events <- runErrorMsg{err: errors.New("agent event stream closed without a terminal event")}
					}
					return
				}
				if readErr != nil {
					m.events <- runErrorMsg{err: readErr}
					return
				}

				switch typed := event.(type) {
				case common.ReasoningDeltaEvent:
					m.events <- reasoningChunkMsg(typed.Delta)
				case common.AssistantTextDeltaEvent:
					m.events <- answerChunkMsg(typed.Delta)
				case common.FinalAnswerCompletedEvent:
					m.events <- finalAnswerMsg(typed.Answer)
				case common.ToolCallStartedEvent:
					m.events <- toolStartedMsg{name: typed.Name, input: typed.Arguments}
				case common.ToolCallCompletedEvent:
					m.events <- toolFinishedMsg{name: typed.Name, result: typed.Result}
				case common.ToolCallFailedEvent:
					m.events <- toolFinishedMsg{name: typed.Name, result: "Error: " + typed.Error}
				case planexecute.PlanCreatedEvent:
					m.events <- planCreatedMsg{plan: typed.Plan}
				case planexecute.PlanRevisedEvent:
					m.events <- planCreatedMsg{plan: typed.Plan, revised: true, reason: typed.Reason}
				case planexecute.StepStartedEvent:
					m.events <- planStepStartedMsg{step: typed.Step}
				case planexecute.StepCompletedEvent:
					m.events <- planStepCompletedMsg{result: typed.Result}
				case common.RunCompletedEvent:
					terminalSeen = true
					usage := typed.Usage
					if usage == nil {
						usage = &common.AgentUsage{}
					}
					m.events <- runFinishedMsg{
						promptTokens:     usage.PromptTokens,
						cachedTokens:     usage.CachedTokens,
						completionTokens: usage.CompletionTokens,
					}
				case common.RunInterruptedEvent:
					terminalSeen = true
					m.events <- runInterruptedMsg{reason: typed.Reason}
				case common.RunCanceledEvent:
					terminalSeen = true
					m.events <- runErrorMsg{err: context.Canceled}
				case common.RunFailedEvent:
					terminalSeen = true
					m.events <- runErrorMsg{err: errors.New(typed.Error)}
				}
			}
		}()
		return runStartedMsg{signature: signature}
	}
}

func (m *model) steer(text string) tea.Cmd {
	uid := m.contextUID
	return func() tea.Msg {
		err := m.agent.Steer(m.ctx, &common.AgentSteerArgs{
			ContextUID: uid,
			UserInputs: []common.AgentUserInput{{Text: text}},
		})
		return steerResultMsg{err: err}
	}
}

func (m *model) waitForEvent() tea.Cmd {
	return func() tea.Msg { return <-m.events }
}

func (m *model) appendText(text string) {
	m.transcript.WriteString(text)
	m.viewport.SetContent(m.transcript.String())
	m.viewport.GotoBottom()
}

func (m *model) closeAnswer() {
	if m.answerOpen {
		m.appendText("\n\n")
		m.answerOpen = false
	}
	m.answerTextOpen = false
}

func (m *model) resize() {
	m.viewport.Width = max(1, m.width-4)
	m.viewport.Height = max(3, m.height-7)
	m.input.Width = max(1, m.width-2)
	m.viewport.SetContent(m.transcript.String())
	m.viewport.GotoBottom()
}

func abbreviate(value string, limit int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
