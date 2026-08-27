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
	candidate := make([]bool, len(conversationMessages))
	detailedMessageCount := 0
	for index, msg := range conversationMessages {
		candidate[index] = isDiscardableDetailedMessage(msg, protectedSkillCallIDs)
		if candidate[index] {
			detailedMessageCount++
		}
	}

	// A call and its output are one replay unit. Only discard a pair when both
	// sides are discardable; otherwise retaining just the output would produce
	// an invalid Responses input item.
	groups := toolPairMessageIndexes(conversationMessages)
	groupByIndex := make(map[int][]int)
	for _, group := range groups {
		for _, index := range group {
			groupByIndex[index] = group
		}
	}

	discardCount := detailedMessageCount / 2
	if discardCount == 0 {
		return messages, 0, 0, 0, nil
	}
	discard := make([]bool, len(conversationMessages))
	remainingToDiscard := discardCount
	for index := range conversationMessages {
		if !candidate[index] {
			continue
		}
		group := groupByIndex[index]
		if len(group) == 0 {
			if remainingToDiscard > 0 {
				discard[index] = true
				remainingToDiscard--
			}
			continue
		}
		if group[0] != index || len(group) > remainingToDiscard {
			continue
		}
		canDiscard := true
		for _, groupIndex := range group {
			if !candidate[groupIndex] {
				canDiscard = false
				break
			}
		}
		if !canDiscard {
			continue
		}
		for _, groupIndex := range group {
			discard[groupIndex] = true
		}
		remainingToDiscard -= len(group)
	}

	retainedConversation := make([]*message.Message, 0, len(conversationMessages)-discardCount)
	for index, msg := range conversationMessages {
		if discard[index] {
			continue
		}
		retainedConversation = append(retainedConversation, msg)
	}
	discardCount -= remainingToDiscard

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
