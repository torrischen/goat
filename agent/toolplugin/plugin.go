package toolplugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"plugin"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/toolplugin/pb"
	"github.com/torrischen/goat/util/logging"

	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/agent/message"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type ToolPlugin interface {
	Init() error
	Name() string
	Description() string
	// Must be contruct by NewToolParameters
	Parameters() common.ToolParameters
	Execute(*common.AgentContext, map[string]any) common.ToolResult
	Ping() error
}

func LoadPluginsFromSharedLib(dir string) ([]ToolPlugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		logging.Errorf("Failed to read plugin directory: %v", err)
		return nil, err
	}

	plugins := make([]ToolPlugin, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		pluginPath := filepath.Join(dir, entry.Name())
		p, err := plugin.Open(pluginPath)
		if err != nil {
			logging.Errorf("Failed to open plugin %s: %v", pluginPath, err)
			continue
		}

		symTool, err := p.Lookup("New")
		if err != nil {
			logging.Errorf("Failed to find Tool symbol in plugin %s: %v", pluginPath, err)
			continue
		}

		newToolFunc, ok := symTool.(func() ToolPlugin)
		if !ok {
			logging.Errorf("Symbol Tool in plugin %s does not implement ToolPlugin interface", pluginPath)
			continue
		}

		tool := newToolFunc()

		if err := tool.Init(); err != nil {
			logging.Errorf("Failed to initialize tool from plugin %s: %v", pluginPath, err)
			continue
		}

		plugins = append(plugins, tool)
		logging.Infof("Successfully loaded plugin: %s", pluginPath)
	}

	return plugins, nil
}

func LoadPluginsFromRPC(address string) (ToolPlugin, io.Closer, error) {
	if address == "" {
		return nil, nil, fmt.Errorf("rpc server address is empty")
	}

	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logging.Errorf("Failed to connect to plugin RPC server %s: %v", address, err)
		return nil, nil, err
	}

	client := pb.NewPluginServiceClient(conn)
	tool := &rpcToolPlugin{
		client: client,
		conn:   conn,
	}

	callCtx := context.Background()
	if err := tool.Init(); err != nil {
		_ = conn.Close()
		logging.Errorf("Failed to init plugin RPC server %s: %v", address, err)
		return nil, nil, err
	}

	nameResp, err := client.Name(callCtx, &emptypb.Empty{})
	if err != nil {
		_ = conn.Close()
		logging.Errorf("Failed to get plugin name from RPC server %s: %v", address, err)
		return nil, nil, err
	}
	tool.name = nameResp.GetName()

	descResp, err := client.Description(callCtx, &emptypb.Empty{})
	if err != nil {
		_ = conn.Close()
		logging.Errorf("Failed to get plugin description from RPC server %s: %v", address, err)
		return nil, nil, err
	}
	tool.description = descResp.GetDescription()

	paramsResp, err := client.Properties(callCtx, &emptypb.Empty{})
	if err != nil {
		_ = conn.Close()
		logging.Errorf("Failed to get plugin parameters from RPC server %s: %v", address, err)
		return nil, nil, err
	}

	toolProperties := make([]common.ToolProperty, 0)
	if paramsResp != nil && paramsResp.GetProperties() != nil {
		pbProperties := paramsResp.GetProperties()
		for _, prop := range pbProperties {
			byteProp, err := sonic.Marshal(prop)
			if err != nil {
				logging.Errorf("Failed to marshal plugin parameter from RPC server %s: %v", address, err)
				continue
			}
			var newProp common.ToolProperty
			if err := sonic.Unmarshal(byteProp, &newProp); err != nil {
				logging.Errorf("Failed to unmarshal plugin parameter from RPC server %s: %v", address, err)
				continue
			}

			toolProperties = append(toolProperties, newProp)
		}
	}
	tool.parameters = common.NewToolParameters(toolProperties...)

	logging.Infof("Successfully loaded plugin from RPC server: %s", address)
	return tool, conn, nil
}

type rpcToolPlugin struct {
	client      pb.PluginServiceClient
	conn        *grpc.ClientConn
	name        string
	description string
	parameters  common.ToolParameters
}

func (t *rpcToolPlugin) Init() error {
	if t.client == nil {
		return fmt.Errorf("rpc plugin client is nil")
	}
	_, err := t.client.Init(context.Background(), &emptypb.Empty{})
	return err
}

func (t *rpcToolPlugin) Name() string {
	return t.name
}

func (t *rpcToolPlugin) Description() string {
	return t.description
}

func (t *rpcToolPlugin) Parameters() common.ToolParameters {
	return t.parameters
}

func (t *rpcToolPlugin) Execute(ctx *common.AgentContext, inputs map[string]any) common.ToolResult {
	if t.client == nil {
		return common.NewDefaultToolResult("rpc plugin client is nil")
	}

	var metaStruct *structpb.Struct
	if ctx != nil {
		metaMap := agentMetaToMap(ctx)
		if metaMap != nil {
			meta, err := structpb.NewStruct(metaMap)
			if err != nil {
				logging.Errorf("rpc plugin Execute: invalid ctx meta: %v", err)
				return common.NewDefaultToolResult(err.Error())
			}
			metaStruct = meta
		}
	}

	var paramsStruct *structpb.Struct
	if inputs != nil {
		params, err := structpb.NewStruct(inputs)
		if err != nil {
			logging.Errorf("rpc plugin Execute: invalid parameters: %v", err)
			return common.NewDefaultToolResult(err.Error())
		}
		paramsStruct = params
	}

	callCtx := context.Background()
	if ctx != nil {
		callCtx = ctx
	}

	result, err := t.client.Execute(callCtx, &pb.ExecuteRequest{
		Ctxmeta:    metaStruct,
		Parameters: paramsStruct,
	})
	if err != nil {
		logging.Errorf("rpc plugin Execute error: %v", err)
		return common.NewDefaultToolResult(err.Error())
	}

	if result == nil {
		return common.NewDefaultToolResult("")
	}

	return newRPCToolResult(result)
}

func (t *rpcToolPlugin) Ping() error {
	if t.client == nil {
		return fmt.Errorf("rpc plugin client is nil")
	}
	_, err := t.client.Ping(context.Background(), &emptypb.Empty{})
	return err
}

type rpcToolResult struct {
	text             string
	structuredResult map[string]any
	imageParts       []*message.ContentBlock
}

func (r *rpcToolResult) ImageParts() []*message.ContentBlock {
	return r.imageParts
}

func (r *rpcToolResult) Usage() *common.AgentUsage {
	return nil
}

func (r *rpcToolResult) String() string {
	if r == nil {
		return ""
	}

	switch {
	case r.text == "" && len(r.structuredResult) == 0:
		return ""
	case r.text == "":
		raw, err := sonic.MarshalIndent(r.structuredResult, "", "  ")
		if err != nil {
			return ""
		}
		return string(raw)
	case len(r.structuredResult) == 0:
		return r.text
	}

	raw, err := sonic.MarshalIndent(struct {
		Result            string         `json:"result"`
		StructuredContent map[string]any `json:"structured_content"`
	}{
		Result:            r.text,
		StructuredContent: r.structuredResult,
	}, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

func newRPCToolResult(result *pb.ExecuteResponse) common.ToolResult {
	if result == nil {
		return common.NewDefaultToolResult("")
	}

	r := &rpcToolResult{
		text: result.GetResult(),
	}

	if structured := result.GetStructuredContent(); structured != nil {
		r.structuredResult = structured.AsMap()
	}

	for _, imagePart := range result.GetImageParts() {
		switch part := imagePart.GetContent().(type) {
		case *pb.ImagePart_ImageUrl:
			if part.ImageUrl == nil || part.ImageUrl.GetUrl() == "" {
				continue
			}
			if part.ImageUrl.GetDetail() != "" {
				r.imageParts = append(r.imageParts, common.ImageURLWithDetailBlock(part.ImageUrl.GetUrl(), part.ImageUrl.GetDetail()))
				continue
			}
			r.imageParts = append(r.imageParts, common.ImageURLBlock(part.ImageUrl.GetUrl()))
		case *pb.ImagePart_Binary:
			if part.Binary == nil || part.Binary.GetMimeType() == "" || len(part.Binary.GetData()) == 0 {
				continue
			}
			r.imageParts = append(r.imageParts, common.BinaryImageBlock(part.Binary.GetMimeType(), part.Binary.GetData()))
		}
	}

	return r
}

func agentMetaToMap(ctx *common.AgentContext) map[string]any {
	meta := ctx.GetAllMeta()
	if len(meta) == 0 {
		return nil
	}

	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k.String()] = v
	}
	return out
}
