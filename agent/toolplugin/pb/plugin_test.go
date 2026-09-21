package pb

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGeneratedMessagesRoundTrip(t *testing.T) {
	meta, _ := structpb.NewStruct(map[string]any{"request": "one"})
	params, _ := structpb.NewStruct(map[string]any{"value": 3.0})
	message := &ExecuteRequest{Ctxmeta: meta, Parameters: params}
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecuteRequest
	if err := proto.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetCtxmeta().AsMap()["request"] != "one" || decoded.GetParameters().AsMap()["value"] != 3.0 {
		t.Fatalf("decoded request = %v", &decoded)
	}
	if decoded.ProtoReflect().Descriptor().FullName() == "" || !strings.Contains(decoded.String(), "request") {
		t.Fatal("protobuf reflection or String failed")
	}
	decoded.Reset()
	if decoded.GetCtxmeta() != nil || decoded.GetParameters() != nil {
		t.Fatal("Reset did not clear request")
	}
	var nilRequest *ExecuteRequest
	if nilRequest.GetCtxmeta() != nil || nilRequest.GetParameters() != nil || nilRequest.ProtoReflect() == nil {
		t.Fatal("nil request getters failed")
	}
}

func TestGeneratedResponseGetters(t *testing.T) {
	name := &NameResponse{Name: "tool"}
	if name.GetName() != "tool" || !strings.Contains(name.String(), "tool") || name.ProtoReflect() == nil {
		t.Fatal("name response failed")
	}
	name.Reset()
	var nilName *NameResponse
	if name.GetName() != "" || nilName.GetName() != "" || nilName.ProtoReflect() == nil {
		t.Fatal("name defaults failed")
	}

	description := &DescriptionResponse{Description: "desc"}
	if description.GetDescription() != "desc" || description.ProtoReflect() == nil || description.String() == "" {
		t.Fatal("description response failed")
	}
	description.Reset()
	var nilDescription *DescriptionResponse
	if nilDescription.GetDescription() != "" || nilDescription.ProtoReflect() == nil {
		t.Fatal("description defaults failed")
	}

	child := &ToolProperty{Name: "child", Type: "string", Required: true, Description: "value"}
	property := &ToolProperty{Name: "parent", Type: "array", Required: true, Description: "items", Items: child, Properties: []*ToolProperty{child}}
	if property.GetName() != "parent" || property.GetType() != "array" || !property.GetRequired() || property.GetDescription() != "items" || property.GetItems() != child || len(property.GetProperties()) != 1 {
		t.Fatalf("property getters failed: %v", property)
	}
	if property.ProtoReflect() == nil || property.String() == "" {
		t.Fatal("property reflection failed")
	}
	property.Reset()
	var nilProperty *ToolProperty
	if nilProperty.GetName() != "" || nilProperty.GetType() != "" || nilProperty.GetRequired() || nilProperty.GetDescription() != "" || nilProperty.GetItems() != nil || nilProperty.GetProperties() != nil || nilProperty.ProtoReflect() == nil {
		t.Fatal("property defaults failed")
	}

	properties := &PropertiesResponse{Properties: []*ToolProperty{child}}
	if len(properties.GetProperties()) != 1 || properties.ProtoReflect() == nil || properties.String() == "" {
		t.Fatal("properties response failed")
	}
	properties.Reset()
	var nilProperties *PropertiesResponse
	if nilProperties.GetProperties() != nil || nilProperties.ProtoReflect() == nil {
		t.Fatal("properties defaults failed")
	}
}

func TestGeneratedImageAndExecuteResponse(t *testing.T) {
	url := &ImageURL{Url: "https://example.test/image", Detail: "high"}
	binary := &BinaryImage{MimeType: "image/png", Data: []byte("data")}
	urlPart := &ImagePart{Content: &ImagePart_ImageUrl{ImageUrl: url}}
	binaryPart := &ImagePart{Content: &ImagePart_Binary{Binary: binary}}
	structured, _ := structpb.NewStruct(map[string]any{"ok": true})
	response := &ExecuteResponse{Result: "done", StructuredContent: structured, ImageParts: []*ImagePart{urlPart, binaryPart}}

	if url.GetUrl() == "" || url.GetDetail() != "high" || binary.GetMimeType() != "image/png" || string(binary.GetData()) != "data" {
		t.Fatal("image getters failed")
	}
	if urlPart.GetImageUrl() != url || urlPart.GetBinary() != nil || binaryPart.GetBinary() != binary || binaryPart.GetImageUrl() != nil || urlPart.GetContent() == nil {
		t.Fatal("image oneof getters failed")
	}
	if response.GetResult() != "done" || response.GetStructuredContent() != structured || len(response.GetImageParts()) != 2 {
		t.Fatal("execute response getters failed")
	}
	for _, message := range []proto.Message{url, binary, urlPart, response} {
		if message.ProtoReflect() == nil || fmt.Sprint(message) == "" {
			t.Fatalf("reflection failed for %T", message)
		}
	}

	url.Reset()
	binary.Reset()
	urlPart.Reset()
	response.Reset()
	var nilURL *ImageURL
	var nilBinary *BinaryImage
	var nilPart *ImagePart
	var nilResponse *ExecuteResponse
	if nilURL.GetUrl() != "" || nilURL.GetDetail() != "" || nilURL.ProtoReflect() == nil || nilBinary.GetMimeType() != "" || nilBinary.GetData() != nil || nilBinary.ProtoReflect() == nil {
		t.Fatal("nil image defaults failed")
	}
	if nilPart.GetContent() != nil || nilPart.GetImageUrl() != nil || nilPart.GetBinary() != nil || nilPart.ProtoReflect() == nil {
		t.Fatal("nil image part defaults failed")
	}
	if nilResponse.GetResult() != "" || nilResponse.GetStructuredContent() != nil || nilResponse.GetImageParts() != nil || nilResponse.ProtoReflect() == nil {
		t.Fatal("nil response defaults failed")
	}
}
