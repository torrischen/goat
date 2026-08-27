package compression

import (
	"context"
	"fmt"
	"strings"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/util/logging"
)

func compressAggressive(
	ctx context.Context,
	client llm.Client,
	messages []*message.Message,
	recentMessages int,
	opts ...llm.Option,
) ([]*message.Message, int, int, int, error) {
	if len(messages) <= 3 {
		// Keep at least system + user + assistant.
		return messages, 0, 0, 0, nil
	}

	systemMessage, conversationMessages := splitSystemMessage(messages)
	toCompress, toKeep := partitionCompressionMessages(conversationMessages, recentMessages)
	if len(toCompress) == 0 {
		return messages, 0, 0, 0, nil
	}

	// Present repeated outcomes from one tool as a single chronological group
	// so the summary merges the tool history instead of treating every result
	// message as an unrelated turn.
	toCompress = mergeSameToolResultMessages(toCompress)
	callNames := collectFunctionToolCallNames(toCompress)
	var compressionPrompt strings.Builder
	compressionPrompt.WriteString("Please summarize the following conversation history concisely, preserving key information. Results in each tool_result_group come from repeated calls to the same tool and should be consolidated without losing distinct outcomes:\n\n")
	for _, msg := range toCompress {
		if appendAggressiveToolResultGroup(&compressionPrompt, msg, callNames) {
			continue
		}
		_, _ = fmt.Fprintf(&compressionPrompt, "[%s]: %s\n", msg.Role, messagePlainText(msg))
	}

	summaryOpts := make([]llm.Option, 0, len(opts)+1)
	summaryOpts = append(summaryOpts, opts...)
	summaryOpts = append(summaryOpts, llm.WithToolChoiceNone())

	raw, err := client.Generate(ctx, []*message.Message{
		message.UserMessage(compressionPrompt.String()),
	}, summaryOpts...)
	if err != nil {
		logging.Errorf("compression: aggressive model call failed: %v", err)
		return messages, 0, 0, 0, err
	}
	if raw == nil {
		logging.Errorf("compression: aggressive model returned no content")
		return messages, 0, 0, 0, fmt.Errorf("return content length 0")
	}

	promptTokens, completionTokens, cachedTokens := messageTokens(raw)
	compressedMessages := make([]*message.Message, 0, 2+len(toKeep))
	if systemMessage != nil {
		compressedMessages = append(compressedMessages, systemMessage)
	}
	compressedMessages = append(
		compressedMessages,
		common.AssistantTextMessage(aggressiveCompressionSummaryPrefix+assistantText(raw)),
	)
	compressedMessages = append(compressedMessages, toKeep...)

	logging.Infof("Aggressively compressed %d messages to %d messages", len(messages), len(compressedMessages))
	return compressedMessages, promptTokens, completionTokens, cachedTokens, nil
}

func appendAggressiveToolResultGroup(
	prompt *strings.Builder,
	msg *message.Message,
	callNames map[string]string,
) bool {
	results, ok := functionToolResultsOnly(msg)
	if !ok {
		return false
	}

	currentName := ""
	for _, result := range results {
		name := resolvedFunctionToolResultName(result, callNames)
		if name == "" {
			name = "unknown"
		}
		if name != currentName {
			_, _ = fmt.Fprintf(prompt, "[tool_result_group name=%q]:\n", name)
			currentName = name
		}
		_, _ = fmt.Fprintf(
			prompt,
			"- call_id=%q result=%s\n",
			result.CallID,
			functionToolResultText(result),
		)
	}
	return true
}
