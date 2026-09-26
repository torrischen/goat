package react

import (
	"context"
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm/llmtest"
	"github.com/torrischen/goat/streaming"
)

func TestGenerateFinalAnswerUsesSystemRequirementsWithoutRepeatingThem(t *testing.T) {
	model := &llmtest.Mock{
		StreamResponses: [][]*message.Message{{message.AssistantTextMessage("done")}},
	}
	agent := &Agent{llmClient: model}
	events := streaming.NewStream[common.AgentEvent](4)
	defer events.Close()

	_, _, err := agent.generateFinalAnswer(
		common.NewAgentContext(context.Background()),
		[]*message.Message{
			message.SystemMessage("## Special Requirements\n- Keep answers concise."),
			message.UserMessage("question"),
		},
		events,
	)
	if err != nil {
		t.Fatalf("generateFinalAnswer() error = %v", err)
	}

	inputs := model.StreamInputs()
	if len(inputs) != 1 {
		t.Fatalf("stream calls = %d, want 1", len(inputs))
	}
	finalMessages := inputs[0]
	if len(finalMessages) != 3 {
		t.Fatalf("final message count = %d, want 3", len(finalMessages))
	}
	if got := finalMessages[len(finalMessages)-1].Text(); got != "Please provide a final answer to the user's question based on the conversation history." {
		t.Fatalf("final prompt = %q", got)
	}
	allText := ""
	for _, msg := range finalMessages {
		allText += msg.Text() + "\n"
	}
	if got := strings.Count(allText, "Keep answers concise."); got != 1 {
		t.Fatalf("special requirement occurrences = %d, want 1", got)
	}
}
