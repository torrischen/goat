package compression

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/llm"
)

func TestCompressMessagesDiscardHalfOnlyDiscardsDetailedProcess(t *testing.T) {
	systemMessage := message.SystemMessage("system")
	firstUserInput := message.UserMessage("first question")
	common.MarkRunStart(firstUserInput, "run-1")
	oldToolCall := discardHalfToolCallMessage("search", "search-1")
	oldToolResult := discardHalfToolResultMessage("search", "search-1", "old details")
	loadSkillsCall := discardHalfToolCallMessage(tools.InternalToolLoadSkills, "skill-1")
	loadSkillsResult := discardHalfToolResultMessage(tools.InternalToolLoadSkills, "skill-1", "skill content")
	recentToolCall := discardHalfToolCallMessage("search", "search-2")
	recentToolResult := discardHalfToolResultMessage("search", "search-2", "recent details")
	readSkillFileCall := discardHalfToolCallMessage(tools.InternalToolReadSpecifiedFileInSkill, "skill-file-1")
	// A provider may omit the result name. The matching protected call ID must
	// still keep the result intact.
	readSkillFileResult := discardHalfToolResultMessage("", "skill-file-1", "reference content")
	firstFinalAnswer := common.AssistantTextMessage("first answer")
	secondUserInput := message.UserMessage("follow-up question")
	common.MarkRunStart(secondUserInput, "run-2")
	secondFinalAnswer := common.AssistantTextMessage("follow-up answer")

	messages := []*message.Message{
		systemMessage,
		firstUserInput,
		oldToolCall,
		oldToolResult,
		loadSkillsCall,
		loadSkillsResult,
		recentToolCall,
		recentToolResult,
		readSkillFileCall,
		readSkillFileResult,
		firstFinalAnswer,
		secondUserInput,
		secondFinalAnswer,
	}

	compressed, promptTokens, completionTokens, cachedTokens, err := Compress(
		context.Background(),
		nil,
		messages,
		common.CompressionOptions{Strategy: common.CompressionStrategyDiscardHalf},
	)
	if err != nil {
		t.Fatalf("compressMessagesDiscardHalf() error = %v", err)
	}
	if promptTokens != 0 || completionTokens != 0 || cachedTokens != 0 {
		t.Fatalf(
			"compressMessagesDiscardHalf() token usage = (%d, %d, %d), want all zero",
			promptTokens,
			completionTokens,
			cachedTokens,
		)
	}

	want := []*message.Message{
		systemMessage,
		firstUserInput,
		loadSkillsCall,
		loadSkillsResult,
		recentToolCall,
		recentToolResult,
		readSkillFileCall,
		readSkillFileResult,
		firstFinalAnswer,
		secondUserInput,
		secondFinalAnswer,
	}
	if len(compressed) != len(want) {
		t.Fatalf("compressMessagesDiscardHalf() retained %d messages, want %d", len(compressed), len(want))
	}
	for index := range want {
		if compressed[index] != want[index] {
			t.Errorf("compressMessagesDiscardHalf() message[%d] was not retained in order", index)
		}
	}
	if got, ok := common.RunUIDFromMessage(compressed[1]); !ok || got != "run-1" {
		t.Fatalf("first run boundary after compression = %q, %v", got, ok)
	}
	if got, ok := common.RunUIDFromMessage(compressed[len(compressed)-2]); !ok || got != "run-2" {
		t.Fatalf("second run boundary after compression = %q, %v", got, ok)
	}
}

func TestCompressMessagesDiscardHalfDoesNothingWithoutDetailedProcess(t *testing.T) {
	messages := []*message.Message{
		message.SystemMessage("system"),
		message.UserMessage("question"),
		discardHalfToolCallMessage(tools.InternalToolLoadSkills, "skill-1"),
		discardHalfToolResultMessage(tools.InternalToolLoadSkills, "skill-1", "skill content"),
		common.AssistantTextMessage("answer"),
	}

	compressed, _, _, _, err := Compress(
		context.Background(),
		nil,
		messages,
		common.CompressionOptions{Strategy: common.CompressionStrategyDiscardHalf},
	)
	if err != nil {
		t.Fatalf("compressMessagesDiscardHalf() error = %v", err)
	}
	if len(compressed) != len(messages) {
		t.Fatalf("compressMessagesDiscardHalf() retained %d messages, want %d", len(compressed), len(messages))
	}
	for index := range messages {
		if compressed[index] != messages[index] {
			t.Errorf("compressMessagesDiscardHalf() message[%d] changed", index)
		}
	}
}

func TestModelBasedCompressionStrategiesPreserveProtectedMessages(t *testing.T) {
	tests := []struct {
		name           string
		strategy       common.CompressionStrategy
		modelResponse  string
		artifactPrefix string
	}{
		{
			name:           "precise",
			strategy:       common.CompressionStrategyPrecise,
			modelResponse:  `{"version":1,"next_source_id":1}`,
			artifactPrefix: compressionCheckpointPrefix,
		},
		{
			name:           "aggressive",
			strategy:       common.CompressionStrategyAggressive,
			modelResponse:  "compressed tool details",
			artifactPrefix: aggressiveCompressionSummaryPrefix,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			systemMessage := message.SystemMessage("system")
			userInput := message.UserMessage("KEEP_USER_INPUT")
			common.MarkRunStart(userInput, "preserved-run")
			oldToolCall := discardHalfToolCallMessage("search", "search-old")
			oldToolResult := discardHalfToolResultMessage("search", "search-old", "COMPRESS_OLD_TOOL_RESULT")
			loadSkillsCall := discardHalfToolCallMessage(tools.InternalToolLoadSkills, "skill-1")
			loadSkillsResult := discardHalfToolResultMessage(
				tools.InternalToolLoadSkills,
				"skill-1",
				"KEEP_LOAD_SKILL_CONTENT",
			)
			readSkillFileCall := discardHalfToolCallMessage(tools.InternalToolReadSpecifiedFileInSkill, "skill-file-1")
			readSkillFileResult := discardHalfToolResultMessage(
				tools.InternalToolReadSpecifiedFileInSkill,
				"skill-file-1",
				"KEEP_READ_SKILL_FILE_CONTENT",
			)
			finalAnswer := common.AssistantTextMessage("KEEP_FINAL_ANSWER")
			recentToolCall := discardHalfToolCallMessage("search", "search-recent")
			recentToolResult := discardHalfToolResultMessage("search", "search-recent", "KEEP_RECENT_TOOL_RESULT")

			messages := []*message.Message{
				systemMessage,
				userInput,
				oldToolCall,
				oldToolResult,
				loadSkillsCall,
				loadSkillsResult,
				readSkillFileCall,
				readSkillFileResult,
				finalAnswer,
				recentToolCall,
				recentToolResult,
			}
			llm := &recordingCompressionModel{
				response: common.AssistantTextMessage(test.modelResponse),
			}

			compressed, _, _, _, err := Compress(
				context.Background(),
				llm,
				messages,
				common.CompressionOptions{
					Strategy:       test.strategy,
					RecentMessages: 2,
				},
			)
			if err != nil {
				t.Fatalf("compressMessages() error = %v", err)
			}

			want := []*message.Message{
				systemMessage,
				userInput,
				loadSkillsCall,
				loadSkillsResult,
				readSkillFileCall,
				readSkillFileResult,
				finalAnswer,
				recentToolCall,
				recentToolResult,
			}
			if len(compressed) != len(want)+1 {
				t.Fatalf("compressMessages() retained %d messages, want %d", len(compressed), len(want)+1)
			}
			if compressed[0] != systemMessage {
				t.Error("compressMessages() did not retain the system message first")
			}
			if !strings.HasPrefix(assistantText(compressed[1]), test.artifactPrefix) {
				t.Errorf("compressMessages() message[1] is not the expected compression artifact")
			}
			for index, message := range want[1:] {
				if compressed[index+2] != message {
					t.Errorf("compressMessages() protected message[%d] was not retained in order", index)
				}
			}
			if got, ok := common.RunUIDFromMessage(compressed[2]); !ok || got != "preserved-run" {
				t.Fatalf("run boundary after %s compression = %q, %v", test.name, got, ok)
			}

			compressionInput := llm.inputText()
			for _, protectedText := range []string{
				"KEEP_USER_INPUT",
				"KEEP_LOAD_SKILL_CONTENT",
				"KEEP_READ_SKILL_FILE_CONTENT",
				"KEEP_FINAL_ANSWER",
				"KEEP_RECENT_TOOL_RESULT",
			} {
				if strings.Contains(compressionInput, protectedText) {
					t.Errorf("compression model input unexpectedly contains protected content %q", protectedText)
				}
			}
			if !strings.Contains(compressionInput, "COMPRESS_OLD_TOOL_RESULT") {
				t.Error("compression model input does not contain the old detailed process")
			}
		})
	}
}

func TestMergeSameToolResultMessagesGroupsByNameWithoutCrossingDurableBoundaries(t *testing.T) {
	firstCall := discardHalfToolCallMessage("search", "search-1")
	firstResult := discardHalfToolResultMessage("search", "search-1", "first result")
	secondCall := discardHalfToolCallMessage("search", "search-2")
	// Exercise providers that omit the result name; the call ID still identifies
	// the result as belonging to the search tool.
	secondResult := discardHalfToolResultMessage("", "search-2", "second result")
	boundary := message.UserMessage("next user turn")
	thirdCall := discardHalfToolCallMessage("search", "search-3")
	thirdResult := discardHalfToolResultMessage("search", "search-3", "third result")
	firstSkillCall := discardHalfToolCallMessage(tools.InternalToolLoadSkills, "skill-1")
	firstSkillResult := discardHalfToolResultMessage(tools.InternalToolLoadSkills, "skill-1", "skill one")
	secondSkillCall := discardHalfToolCallMessage(tools.InternalToolLoadSkills, "skill-2")
	secondSkillResult := discardHalfToolResultMessage(tools.InternalToolLoadSkills, "skill-2", "skill two")

	messages := []*message.Message{
		firstCall,
		firstResult,
		secondCall,
		secondResult,
		boundary,
		thirdCall,
		thirdResult,
		firstSkillCall,
		firstSkillResult,
		secondSkillCall,
		secondSkillResult,
	}
	merged := mergeSameToolResultMessages(messages)

	if len(merged) != len(messages)-1 {
		t.Fatalf("mergeSameToolResultMessages() returned %d messages, want %d", len(merged), len(messages)-1)
	}
	if merged[0] != firstCall || merged[1] != secondCall {
		t.Fatal("mergeSameToolResultMessages() did not retain tool calls in order")
	}
	mergedResults, ok := functionToolResultsOnly(merged[2])
	if !ok || len(mergedResults) != 2 {
		t.Fatalf("merged result message contains %d results, want 2", len(mergedResults))
	}
	if mergedResults[0] != firstResult.Blocks[0].ToolResult ||
		mergedResults[1] != secondResult.Blocks[0].ToolResult {
		t.Fatal("merged result message did not preserve chronological result blocks")
	}
	if len(firstResult.Blocks) != 1 || len(secondResult.Blocks) != 1 {
		t.Fatal("mergeSameToolResultMessages() mutated an input message")
	}

	wantUnchanged := []*message.Message{
		boundary,
		thirdCall,
		thirdResult,
		firstSkillCall,
		firstSkillResult,
		secondSkillCall,
		secondSkillResult,
	}
	for index, want := range wantUnchanged {
		if merged[index+3] != want {
			t.Errorf("mergeSameToolResultMessages() changed boundary/protected message %d", index)
		}
	}
}

func TestModelBasedCompressionGroupsSameToolResultsBeforeSummarizing(t *testing.T) {
	tests := []struct {
		name          string
		strategy      common.CompressionStrategy
		modelResponse string
		assertInput   func(*testing.T, string)
	}{
		{
			name:          "precise",
			strategy:      common.CompressionStrategyPrecise,
			modelResponse: `{"version":1,"next_source_id":1}`,
			assertInput: func(t *testing.T, input string) {
				t.Helper()
				const marker = "New conversation records to merge:\n"
				markerIndex := strings.Index(input, marker)
				if markerIndex < 0 {
					t.Fatalf("precise input does not contain records marker: %s", input)
				}
				var records []compressionRecord
				if err := json.Unmarshal([]byte(input[markerIndex+len(marker):]), &records); err != nil {
					t.Fatalf("parse precise compression records: %v", err)
				}
				groupCount := 0
				for _, record := range records {
					if len(record.ToolResults) == 0 {
						continue
					}
					groupCount++
					if len(record.ToolResults) != 2 {
						t.Errorf("precise tool-result group has %d results, want 2", len(record.ToolResults))
					}
					for _, result := range record.ToolResults {
						if result.Name != "search" {
							t.Errorf("precise tool-result name = %q, want search", result.Name)
						}
					}
				}
				if groupCount != 1 {
					t.Errorf("precise input has %d tool-result groups, want 1", groupCount)
				}
			},
		},
		{
			name:          "aggressive",
			strategy:      common.CompressionStrategyAggressive,
			modelResponse: "summary",
			assertInput: func(t *testing.T, input string) {
				t.Helper()
				if count := strings.Count(input, `[tool_result_group name="search"]`); count != 1 {
					t.Errorf("aggressive input has %d search result groups, want 1: %s", count, input)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := []*message.Message{
				message.SystemMessage("system"),
				discardHalfToolCallMessage("search", "search-1"),
				discardHalfToolResultMessage("search", "search-1", "FIRST_SEARCH_RESULT"),
				discardHalfToolCallMessage("search", "search-2"),
				discardHalfToolResultMessage("", "search-2", "SECOND_SEARCH_RESULT"),
				message.UserMessage("keep user input"),
				common.AssistantTextMessage("keep final answer"),
			}
			llm := &recordingCompressionModel{response: common.AssistantTextMessage(test.modelResponse)}

			_, _, _, _, err := Compress(
				context.Background(),
				llm,
				messages,
				common.CompressionOptions{Strategy: test.strategy, RecentMessages: 1},
			)
			if err != nil {
				t.Fatalf("Compress() error = %v", err)
			}
			if len(llm.inputs) != 1 || len(llm.inputs[0]) == 0 {
				t.Fatalf("compression model received %d inputs, want one non-empty input", len(llm.inputs))
			}
			input := messagePlainText(llm.inputs[0][len(llm.inputs[0])-1])
			test.assertInput(t, input)
			for _, resultText := range []string{"FIRST_SEARCH_RESULT", "SECOND_SEARCH_RESULT"} {
				if !strings.Contains(input, resultText) {
					t.Errorf("compression input lost result %q", resultText)
				}
			}
		})
	}
}

func TestCompressDiscardHalfMergesRetainedSameToolResults(t *testing.T) {
	systemMessage := message.SystemMessage("system")
	firstSearchCall := discardHalfToolCallMessage("search", "search-1")
	// The discarded prefix can contain the call metadata needed to identify a
	// retained provider result whose name is omitted, so discard_half resolves
	// names from the full pre-discard conversation.
	keptFirstResult := discardHalfToolResultMessage("", "search-1", "first kept result")
	keptSecondCall := discardHalfToolCallMessage("search", "search-2")
	keptSecondResult := discardHalfToolResultMessage("", "search-2", "second kept result")
	keptThirdCall := discardHalfToolCallMessage("search", "search-3")
	keptThirdResult := discardHalfToolResultMessage("", "search-3", "third kept result")
	messages := []*message.Message{
		systemMessage,
		discardHalfToolCallMessage("old", "old-1"),
		discardHalfToolResultMessage("old", "old-1", "old result one"),
		discardHalfToolCallMessage("old", "old-2"),
		discardHalfToolResultMessage("old", "old-2", "old result two"),
		firstSearchCall,
		keptFirstResult,
		keptSecondCall,
		keptSecondResult,
		keptThirdCall,
		keptThirdResult,
	}

	compressed, _, _, _, err := Compress(
		context.Background(),
		nil,
		messages,
		common.CompressionOptions{Strategy: common.CompressionStrategyDiscardHalf},
	)
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if len(compressed) != 4 {
		t.Fatalf("Compress() retained %d messages, want 4", len(compressed))
	}
	if compressed[0] != systemMessage || compressed[1] != keptSecondCall || compressed[2] != keptThirdCall {
		t.Fatal("Compress() did not preserve retained system/tool-call messages in order")
	}
	results, ok := functionToolResultsOnly(compressed[3])
	if !ok || len(results) != 3 {
		t.Fatalf("Compress() merged result message has %d results, want 3", len(results))
	}
	if results[0] != keptFirstResult.Blocks[0].ToolResult ||
		results[1] != keptSecondResult.Blocks[0].ToolResult ||
		results[2] != keptThirdResult.Blocks[0].ToolResult {
		t.Fatal("Compress() did not retain all same-tool results in chronological order")
	}
}

func TestPartitionCompressionMessagesCanRecompressOldArtifacts(t *testing.T) {
	oldSummary := common.AssistantTextMessage(aggressiveCompressionSummaryPrefix + "old details")
	userInput := message.UserMessage("question")
	finalAnswer := common.AssistantTextMessage("answer")

	toCompress, toKeep := partitionCompressionMessages(
		[]*message.Message{oldSummary, userInput, finalAnswer},
		0,
	)
	if len(toCompress) != 1 || toCompress[0] != oldSummary {
		t.Fatalf("partitionCompressionMessages() did not select the old summary for recompression")
	}
	if len(toKeep) != 2 || toKeep[0] != userInput || toKeep[1] != finalAnswer {
		t.Fatalf("partitionCompressionMessages() did not preserve user input and final answer")
	}
}

type recordingCompressionModel struct {
	response *message.Message
	inputs   [][]*message.Message
}

func (m *recordingCompressionModel) ModelID() string { return "test-model" }

func (m *recordingCompressionModel) Generate(
	_ context.Context,
	input []*message.Message,
	_ ...llm.CallOption,
) (*message.Message, error) {
	m.inputs = append(m.inputs, append([]*message.Message(nil), input...))
	return m.response, nil
}

func (m *recordingCompressionModel) Stream(
	_ context.Context,
	_ []*message.Message,
	_ ...llm.CallOption,
) (llm.StreamReader, error) {
	return nil, errors.New("stream is not implemented")
}

func (m *recordingCompressionModel) inputText() string {
	var text strings.Builder
	for _, input := range m.inputs {
		for _, message := range input {
			text.WriteString(messagePlainText(message))
			text.WriteByte('\n')
		}
	}
	return text.String()
}

func discardHalfToolCallMessage(name, callID string) *message.Message {
	return &message.Message{
		Role: message.RoleAssistant,
		Blocks: []*message.ContentBlock{{Kind: message.BlockToolCall, ToolCall: &message.ToolCall{
			CallID: callID, Name: name, Arguments: `{}`,
		}}},
	}
}

func discardHalfToolResultMessage(name, callID, text string) *message.Message {
	return common.FunctionToolResultMessage(&message.ToolResult{
		CallID: callID, Name: name,
		Content: []*message.ToolResultContent{{Kind: message.ToolResultText, Text: &message.TextData{Text: text}}},
	})
}
