package toolplugin

import (
	"testing"

	"github.com/torrischen/goat/agent/toolplugin/pb"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestRPCToolResultImageParts(t *testing.T) {
	r, ok := newRPCToolResult(&pb.ExecuteResponse{
		Result: "generated chart",
		ImageParts: []*pb.ImagePart{
			{
				Content: &pb.ImagePart_ImageUrl{
					ImageUrl: &pb.ImageURL{
						Url:    "https://example.com/chart.png",
						Detail: "high",
					},
				},
			},
			{
				Content: &pb.ImagePart_Binary{
					Binary: &pb.BinaryImage{
						MimeType: "image/png",
						Data:     []byte("hello"),
					},
				},
			},
		},
	}).(*rpcToolResult)
	if !ok {
		t.Fatal("expected rpcToolResult")
	}

	parts := r.ImageParts()
	if len(parts) != 2 {
		t.Fatalf("expected 2 image parts, got %d", len(parts))
	}

	urlPart := parts[0].Image
	if urlPart == nil {
		t.Fatalf("expected first part to be UserInputImage, got %#v", parts[0])
	}
	if urlPart.URL != "https://example.com/chart.png" || string(urlPart.Detail) != "high" {
		t.Fatalf("unexpected image url part: %#v", urlPart)
	}

	binaryPart := parts[1].Image
	if binaryPart == nil {
		t.Fatalf("expected second part to be UserInputImage, got %#v", parts[1])
	}
	if binaryPart.MIMEType != "image/png" || binaryPart.Base64Data != "aGVsbG8=" {
		t.Fatalf("unexpected binary part: %#v", binaryPart)
	}
}

func TestRPCToolResultStringUsesStructuredContent(t *testing.T) {
	structured, err := structpb.NewStruct(map[string]any{
		"summary": "done",
	})
	if err != nil {
		t.Fatalf("unexpected struct build error: %v", err)
	}

	r, ok := newRPCToolResult(&pb.ExecuteResponse{
		StructuredContent: structured,
	}).(*rpcToolResult)
	if !ok {
		t.Fatal("expected rpcToolResult")
	}

	got := r.String()
	want := "{\n  \"summary\": \"done\"\n}"
	if got != want {
		t.Fatalf("unexpected string output:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRPCToolResultStringIncludesResultAndStructuredContent(t *testing.T) {
	structured, err := structpb.NewStruct(map[string]any{
		"summary": "kept",
	})
	if err != nil {
		t.Fatalf("unexpected struct build error: %v", err)
	}

	r, ok := newRPCToolResult(&pb.ExecuteResponse{
		Result:            "plain text",
		StructuredContent: structured,
	}).(*rpcToolResult)
	if !ok {
		t.Fatal("expected rpcToolResult")
	}

	got := r.String()
	want := "{\n  \"result\": \"plain text\",\n  \"structured_content\": {\n    \"summary\": \"kept\"\n  }\n}"
	if got != want {
		t.Fatalf("unexpected string output:\nwant: %s\ngot:  %s", want, got)
	}
}
