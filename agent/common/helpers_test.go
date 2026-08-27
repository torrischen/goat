package common

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/torrischen/goat/agent/message"
)

func TestContentAndMessageHelpers(t *testing.T) {
	if got := TextBlock("user").Text.Text; got != "user" {
		t.Fatalf("TextBlock text = %q", got)
	}
	if got := ReasoningBlock("think").Reasoning.Text; got != "think" {
		t.Fatalf("ReasoningBlock text = %q", got)
	}
	if got := ImageURLBlock("https://example.test/image.png").Image.URL; got != "https://example.test/image.png" {
		t.Fatalf("ImageURLBlock URL = %q", got)
	}
	image := ImageURLWithDetailBlock("url", "high").Image
	if image.URL != "url" || image.Detail != "high" {
		t.Fatalf("image = %+v", image)
	}
	binary := BinaryImageBlock("image/png", []byte("data")).Image
	if binary.MIMEType != "image/png" || binary.Base64Data != base64.StdEncoding.EncodeToString([]byte("data")) {
		t.Fatalf("binary image = %+v", binary)
	}
	encoded := Base64ImageBlock("image/jpeg", "encoded").Image
	if encoded.MIMEType != "image/jpeg" || encoded.Base64Data != "encoded" {
		t.Fatalf("base64 image = %+v", encoded)
	}

	roles := []message.Role{message.RoleUser, message.RoleAssistant, message.RoleSystem}
	for _, role := range roles {
		if got := TextMessage(role, "text").Role; got != role {
			t.Fatalf("TextMessage(%s) role = %s", role, got)
		}
	}
	result := &message.ToolResult{}
	if got := FunctionToolResultMessage(result); got.Role != message.RoleUser || got.Blocks[0].ToolResult != result {
		t.Fatalf("FunctionToolResultMessage() = %+v", got)
	}

	messages := []*message.Message{message.UserMessage("hello")}
	clone := CloneAgenticMessages(messages)
	clone[0] = nil
	if messages[0] == nil {
		t.Fatal("CloneAgenticMessages did not copy the slice")
	}
	if got := CloneAgenticMessages(nil); got == nil || len(got) != 0 {
		t.Fatalf("CloneAgenticMessages(nil) = %#v", got)
	}
}

func TestAgentContextMetadataAndInterrupt(t *testing.T) {
	if InternalToolPlanMetaKey.String() != "current_plan" {
		t.Fatalf("meta key = %q", InternalToolPlanMetaKey.String())
	}
	ctx := NewAgentContext(context.Background())
	ctx.SetMeta(InternalToolPlanMetaKey, "plan")
	if got := ctx.GetMeta(InternalToolPlanMetaKey); got != "plan" {
		t.Fatalf("GetMeta = %v", got)
	}
	snapshot := ctx.GetAllMeta()
	snapshot[InternalToolPlanMetaKey] = "changed"
	if ctx.GetMeta(InternalToolPlanMetaKey) != "plan" {
		t.Fatal("GetAllMeta exposed the backing map")
	}
	ctx.DeleteMeta(InternalToolPlanMetaKey)
	if ctx.GetMeta(InternalToolPlanMetaKey) != nil {
		t.Fatal("DeleteMeta did not remove value")
	}
	if ConsumeInterruptSignal(nil) {
		t.Fatal("nil context reported an interrupt")
	}
	var nilContext *AgentContext
	nilContext.signalInterrupt()
	ctx.signalInterrupt()
	if !ConsumeInterruptSignal(ctx) || ConsumeInterruptSignal(ctx) {
		t.Fatal("interrupt signal was not consumed exactly once")
	}
}

func TestUsage(t *testing.T) {
	if NewAgentUsage(0, 0, 0) != nil || (*AgentUsage)(nil).Clone() != nil {
		t.Fatal("zero or nil usage should return nil")
	}
	usage := NewAgentUsage(1, 2, 3)
	clone := usage.Clone()
	clone.Add(NewAgentUsage(4, 5, 6))
	if !reflect.DeepEqual(clone, &AgentUsage{PromptTokens: 5, CachedTokens: 7, CompletionTokens: 9}) {
		t.Fatalf("combined usage = %+v", clone)
	}
	usage.Add(nil)
	(*AgentUsage)(nil).Add(usage)
}

func TestNamesSkillsAndResults(t *testing.T) {
	if got := SanitizeToolName("hello world/工具"); got != "hello_world___" {
		t.Fatalf("SanitizeToolName = %q", got)
	}
	if SanitizeToolName("") != "" || SanitizeToolName("valid-1.name") != "valid-1.name" {
		t.Fatal("valid tool name was changed")
	}
	tool := NewDefaultTool("old", "description", nil, func(*AgentContext, map[string]any) ToolResult {
		return NewDefaultToolResult("ok")
	})
	if WrapToolName(nil, "new") != nil || WrapToolName(tool, "") != tool || WrapToolName(tool, "old") != tool {
		t.Fatal("WrapToolName changed a no-op input")
	}
	wrapped := WrapToolName(tool, "new")
	if wrapped.Name() != "new" || wrapped.Description() != "description" || wrapped.Execute(nil, nil).String() != "ok" {
		t.Fatal("wrapped tool did not delegate")
	}

	header, ok := ExtractSkillHeader("intro\n---\nname: test\ndescription: demo\n---\nbody")
	if !ok || header != "name: test\ndescription: demo" {
		t.Fatalf("ExtractSkillHeader = %q, %v", header, ok)
	}
	for _, input := range []string{"body", "---\nno ending"} {
		if _, ok := ExtractSkillHeader(input); ok {
			t.Fatalf("invalid header %q accepted", input)
		}
	}
	if ContextUID("id").String() != "id" {
		t.Fatal("ContextUID.String returned the wrong value")
	}
	result := NewDefaultToolResult("text")
	if result.String() != "text" || result.ImageParts() != nil || result.Usage() != nil {
		t.Fatalf("default result = %+v", result)
	}
	firstUsage := NewAgentUsage(1, 2, 3)
	if got := result.AddUsage(firstUsage).AddUsage(NewAgentUsage(4, 5, 6)).Usage(); !reflect.DeepEqual(got, &AgentUsage{
		PromptTokens: 5, CachedTokens: 7, CompletionTokens: 9,
	}) {
		t.Fatalf("default result usage = %+v", got)
	}
	if !reflect.DeepEqual(firstUsage, &AgentUsage{PromptTokens: 1, CachedTokens: 2, CompletionTokens: 3}) {
		t.Fatalf("AddUsage mutated its input = %+v", firstUsage)
	}
	result.AddUsage(nil)
	var nilResult *DefaultToolResult
	if nilResult.AddUsage(firstUsage) != nil || nilResult.Usage() != nil {
		t.Fatal("nil default result should ignore usage")
	}
	multi := &MultimodalToolResult{Text: "text", Images: []*message.ContentBlock{TextBlock("image")}}
	if multi.String() != "text" || len(multi.ImageParts()) != 1 || multi.Usage() != nil {
		t.Fatalf("multimodal result = %+v", multi)
	}
}
