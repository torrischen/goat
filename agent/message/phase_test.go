package message

import "testing"

func phasedText(text string, phase TextPhase) *Message {
	block := TextBlock(text)
	block.Text.Phase = phase
	return &Message{Role: RoleAssistant, Blocks: []*ContentBlock{block}}
}

func TestConcatKeepsTextPhasesSeparate(t *testing.T) {
	msg := Concat([]*Message{
		{Role: RoleAssistant, Blocks: []*ContentBlock{ReasoningBlock("think")}},
		phasedText("I'll check ", TextPhaseCommentary),
		phasedText("the metrics.", TextPhaseCommentary),
		phasedText("Root cause: ", TextPhaseFinalAnswer),
		phasedText("flag gateway.", TextPhaseFinalAnswer),
	})

	var texts []*TextData
	for _, b := range msg.Blocks {
		if b.Kind == BlockText {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("text blocks = %d, want 2", len(texts))
	}
	if texts[0].Text != "I'll check the metrics." || texts[0].Phase != TextPhaseCommentary {
		t.Fatalf("first block = %+v", texts[0])
	}
	if texts[1].Text != "Root cause: flag gateway." || texts[1].Phase != TextPhaseFinalAnswer {
		t.Fatalf("second block = %+v", texts[1])
	}
	if msg.Blocks[0].Kind != BlockReasoning {
		t.Fatalf("first block kind = %s, want reasoning", msg.Blocks[0].Kind)
	}
	if got := msg.AnswerText(); got != "Root cause: flag gateway." {
		t.Fatalf("AnswerText() = %q", got)
	}
	if got := msg.Text(); got != "I'll check the metrics.Root cause: flag gateway." {
		t.Fatalf("Text() = %q", got)
	}
}

func TestConcatMergesUnlabelledTextIntoOneBlock(t *testing.T) {
	msg := Concat([]*Message{AssistantTextMessage("a"), AssistantTextMessage("b")})
	if len(msg.Blocks) != 1 || msg.Blocks[0].Text.Text != "ab" || msg.Blocks[0].Text.Phase != TextPhaseUnspecified {
		t.Fatalf("blocks = %+v", msg.Blocks)
	}
}

func TestTextSegmentsResolveUnlabelledPhaseFromToolCalls(t *testing.T) {
	toolTurn := &Message{Role: RoleAssistant, Blocks: []*ContentBlock{
		TextBlock("I'll gather evidence."),
		{Kind: BlockToolCall, ToolCall: &ToolCall{CallID: "c1", Name: "get_metrics"}},
	}}
	segments := toolTurn.TextSegments()
	if len(segments) != 1 || segments[0].Phase != TextPhaseCommentary {
		t.Fatalf("tool turn segments = %+v", segments)
	}
	if got := toolTurn.AnswerText(); got != "" {
		t.Fatalf("tool turn AnswerText() = %q, want empty", got)
	}

	answer := AssistantTextMessage("Done.")
	segments = answer.TextSegments()
	if len(segments) != 1 || segments[0].Phase != TextPhaseFinalAnswer {
		t.Fatalf("answer segments = %+v", segments)
	}
	if got := answer.AnswerText(); got != "Done." {
		t.Fatalf("answer AnswerText() = %q", got)
	}
}

func TestTextSegmentsKeepProviderPhase(t *testing.T) {
	// A provider may label text as the final answer even in a turn that also
	// calls tools; an explicit label always wins over the structural fallback.
	msg := &Message{Role: RoleAssistant, Blocks: []*ContentBlock{
		phasedText("answer", TextPhaseFinalAnswer).Blocks[0],
		{Kind: BlockToolCall, ToolCall: &ToolCall{CallID: "c1", Name: "t"}},
	}}
	segments := msg.TextSegments()
	if len(segments) != 1 || segments[0].Phase != TextPhaseFinalAnswer {
		t.Fatalf("segments = %+v", segments)
	}
}
