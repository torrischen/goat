# Tool Plugin Cookbook

## 1. Go Example

```go
package main

import (
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/toolplugin"
)

var _ toolplugin.ToolPlugin = (*Tool)(nil)

type Tool struct{}

func (t *Tool) Name() string {
	return "english-translate"
}

func (t *Tool) Description() string {
	return "A tool for translating text to English."
}

func (t *Tool) Parameters() common.ToolParameters {
	return common.NewToolParameters(
		common.ToolProperty{
			Name:        "text",
			Type:        "string",
			Description: "The text to be translated to English.",
			Required:    true,
		},
	)
}

func (t *Tool) Execute(actx *common.AgentContext, a map[string]any) common.ToolResult {
	return common.NewDefaultToolResult("Hello")
}

func (t *Tool) Init() error {
	return nil
}

func (t *Tool) Ping() error {
	return nil
}

func New() toolplugin.ToolPlugin {
	return &Tool{}
}

func main() {}
```

### Build

```bash
go build -buildmode=plugin -o english-translate.so tool.go
```

## 2. Python Example

```python
# server.py
# dependencies:
#   pip install grpcio grpcio-tools protobuf
#
# generate gRPC bindings:
#   python -m grpc_tools.protoc -I. \
#     --python_out=. --grpc_python_out=. \
#     -I$(python -c "import pkgutil,grpc_tools,os; import grpc_tools; print(os.path.dirname(grpc_tools.__file__) + '/_proto')") \
#     plugin.proto
#
# run:
#   python server.py

from concurrent import futures
import grpc

from google.protobuf import empty_pb2, struct_pb2

import plugin_pb2
import plugin_pb2_grpc

class PluginService(plugin_pb2_grpc.PluginServiceServicer):
    def Init(self, request, context):
        return empty_pb2.Empty()

    def Ping(self, request, context):
        return empty_pb2.Empty()

    def Name(self, request, context):
        return plugin_pb2.NameResponse(name="EnglishTranslator")

    def Description(self, request, context):
        return plugin_pb2.DescriptionResponse(description="Help you translate text into English.")

    def Properties(self, request, context):
        props = [
            plugin_pb2.ToolProperty(
                name="query",
                type="array",
                required=True,
                items=plugin_pb2.ToolProperty(
                    type="string",
                    required=True,
                ),
                description="Search query text",
            ),
        ]
        return plugin_pb2.PropertiesResponse(properties=props)

    def Execute(self, request, context):
        print("Received query", request.parameters.fields.get("query"))
        structured = struct_pb2.Struct()
        structured.update(
            {
                "translated_text": ["Hello", "World"],
            }
        )
        return plugin_pb2.ExecuteResponse(
            result="Translated 2 values.",
            structured_content=structured,
            image_parts=[
                plugin_pb2.ImagePart(
                    image_url=plugin_pb2.ImageURL(
                        url="https://example.com/preview.png",
                        detail="high",
                    )
                ),
                plugin_pb2.ImagePart(
                    binary=plugin_pb2.BinaryImage(
                        mime_type="image/png",
                        data=b"<raw-png-bytes>",
                    )
                ),
            ],
        )

def serve(host="0.0.0.0", port=50051):
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    plugin_pb2_grpc.add_PluginServiceServicer_to_server(PluginService(), server)
    server.add_insecure_port(f"{host}:{port}")
    server.start()
    print(f"PluginService test server listening on {host}:{port}")
    server.wait_for_termination()

if __name__ == "__main__":
    serve()
```

### Setup and generate bindings

```bash
uv venv && uv pip install grpcio grpcio-tools protobuf

python -m grpc_tools.protoc -I. \
  --python_out=. --grpc_python_out=. \
  -I$(python -c "import pkgutil,grpc_tools,os; import grpc_tools; print(os.path.dirname(grpc_tools.__file__) + '/_proto')") \
  plugin.proto
```

## RPC Result Format

`Execute` now returns `ExecuteResponse`:

```json
{
  "result": "optional plain-text summary",
  "structured_content": {
    "any": "json-like object"
  },
  "image_parts": [
    {
      "image_url": {
        "url": "https://example.com/image.png",
        "detail": "high"
      }
    },
    {
      "binary": {
        "mime_type": "image/png",
        "data": "<raw bytes>"
      }
    }
  ]
}
```
