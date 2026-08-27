package compression

import (
	"github.com/torrischen/goat/util/logging"

	"github.com/torrischen/goat/agent/message"
)

func compressDiscardHalf(messages []*message.Message) ([]*message.Message, int, int, int, error) {
	systemMessage, conversationMessages := splitSystemMessage(messages)
	if len(conversationMessages) <= 1 {
		return messages, 0, 0, 0, nil
	}

	protectedSkillCallIDs := collectProtectedSkillCallIDs(conversationMessages)
	toolCallNames := collectFunctionToolCallNames(conversationMessages)
	detailedMessageCount := 0
	for _, msg := range conversationMessages {
		if isDiscardableDetailedMessage(msg, protectedSkillCallIDs) {
			detailedMessageCount++
		}
	}

	// Discard only the oldest half of the detailed tool process. User inputs,
	// final answers, and skill-loading/reading messages remain byte-for-byte intact.
	discardCount := detailedMessageCount / 2
	if discardCount == 0 {
		return messages, 0, 0, 0, nil
	}

	retainedConversation := make([]*message.Message, 0, len(conversationMessages)-discardCount)
	remainingToDiscard := discardCount
	for _, msg := range conversationMessages {
		if remainingToDiscard > 0 && isDiscardableDetailedMessage(msg, protectedSkillCallIDs) {
			remainingToDiscard--
			continue
		}
		retainedConversation = append(retainedConversation, msg)
	}

	// The retained half can still contain repeated ordinary-tool outputs.
	// Coalesce those messages while keeping every call ID/result block;
	// protected and durable messages remain boundaries and stay intact.
	beforeMergeCount := len(retainedConversation)
	retainedConversation = mergeSameToolResultMessagesWithCallNames(retainedConversation, toolCallNames)
	mergedResultMessageCount := beforeMergeCount - len(retainedConversation)

	compressedMessages := make([]*message.Message, 0, len(retainedConversation)+1)
	if systemMessage != nil {
		compressedMessages = append(compressedMessages, systemMessage)
	}
	compressedMessages = append(compressedMessages, retainedConversation...)

	logging.Infof(
		"Discarded %d of %d detailed-process messages, merged %d same-tool result messages, and retained %d messages",
		discardCount,
		detailedMessageCount,
		mergedResultMessageCount,
		len(compressedMessages),
	)
	return compressedMessages, 0, 0, 0, nil
}
