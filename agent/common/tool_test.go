package common

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic"
)

func TestToolParameters(t *testing.T) {
	params := NewToolParameters(
		ToolProperty{Name: "name", Type: "string", Required: true, Description: "a name"},
		ToolProperty{Name: "items", Type: "array", Items: &ToolProperty{Type: "object", Properties: []ToolProperty{
			{Name: "id", Type: "integer", Required: true},
			{Name: "label", Type: "string"},
		}}},
		ToolProperty{Name: "anything", Type: "array"},
	)
	if params["type"] != "object" {
		t.Fatalf("schema = %#v", params)
	}
	required := params["required"].([]string)
	if !reflect.DeepEqual(required, []string{"name"}) {
		t.Fatalf("required = %v", required)
	}
	properties := params["properties"].(map[string]any)
	items := properties["items"].(map[string]any)["items"].(map[string]any)
	if !reflect.DeepEqual(items["required"], []string{"id"}) {
		t.Fatalf("nested required = %v", items["required"])
	}
	if got := properties["anything"].(map[string]any)["items"]; !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("default array items = %#v", got)
	}
}

func TestDefaultToolSchemaMutation(t *testing.T) {
	tool := NewDefaultTool("test", "description", nil, nil)
	if tool.Parameters() != nil {
		t.Fatal("new tool unexpectedly has parameters")
	}
	tool.AddProperty("first", "string", true)
	tool.AddProperty("first", "string", true)
	tool.AddProperty("second", "integer", false)
	if required := tool.Parameters()["required"].([]string); !reflect.DeepEqual(required, []string{"first"}) {
		t.Fatalf("required = %v", required)
	}

	tool.SetParameters(map[string]any{
		"properties": map[string]any{
			"nested": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "string"}}},
			"list":   map[string]any{"items": map[string]any{"type": "number"}},
		},
	})
	if tool.Parameters()["type"] != "object" {
		t.Fatalf("normalized schema = %#v", tool.Parameters())
	}
	props := tool.Parameters()["properties"].(map[string]any)
	if props["nested"].(map[string]any)["type"] != "object" || props["list"].(map[string]any)["type"] != "array" {
		t.Fatalf("nested schema = %#v", props)
	}

	encoded := tool.String()
	var value map[string]any
	if err := sonic.UnmarshalString(encoded, &value); err != nil {
		t.Fatalf("tool String is invalid JSON: %v", err)
	}
	if value["tool_name"] != "test" {
		t.Fatalf("serialized tool = %v", value)
	}
}

func TestNormalizeToolSchemaEdgeCases(t *testing.T) {
	if got := normalizeToolSchema(nil); got["type"] != "object" || got["properties"] == nil {
		t.Fatalf("normalize nil = %#v", got)
	}
	object := normalizeSchemaMap(map[string]any{"type": "object", "properties": "invalid"})
	if _, ok := object["properties"].(map[string]any); !ok {
		t.Fatalf("object properties = %#v", object["properties"])
	}
	array := normalizeSchemaMap(map[string]any{"type": "array"})
	if _, ok := array["items"].(map[string]any); !ok {
		t.Fatalf("array items = %#v", array["items"])
	}
	scalar := normalizeSchemaMap(map[string]any{"type": "string"})
	if scalar["type"] != "string" {
		t.Fatalf("scalar = %#v", scalar)
	}
	if got := normalizeSchemaMap(nil); len(got) != 0 {
		t.Fatalf("normalize nil map = %#v", got)
	}

	tool := NewDefaultTool("test", "", map[string]any{"type": "object", "required": []any{"one", 2}}, nil)
	tool.AddProperty("two", "string", true)
	if got := tool.Parameters()["required"]; !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("converted required = %#v", got)
	}
}
