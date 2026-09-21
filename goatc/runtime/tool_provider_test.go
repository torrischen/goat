package runtime

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	pluginpb "github.com/torrischen/goat/agent/toolplugin/pb"
	"github.com/torrischen/goat/goatc/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/types/known/emptypb"
)

type testPluginService struct {
	pluginpb.UnimplementedPluginServiceServer
	name      string
	pingErr   error
	initCalls atomic.Int32
	pingCalls atomic.Int32
}

func (s *testPluginService) Init(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	s.initCalls.Add(1)
	return &emptypb.Empty{}, nil
}

func (s *testPluginService) Name(context.Context, *emptypb.Empty) (*pluginpb.NameResponse, error) {
	return &pluginpb.NameResponse{Name: s.name}, nil
}

func (s *testPluginService) Description(context.Context, *emptypb.Empty) (*pluginpb.DescriptionResponse, error) {
	return &pluginpb.DescriptionResponse{Description: "test tool"}, nil
}

func (s *testPluginService) Properties(context.Context, *emptypb.Empty) (*pluginpb.PropertiesResponse, error) {
	return &pluginpb.PropertiesResponse{}, nil
}

func (s *testPluginService) Ping(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	s.pingCalls.Add(1)
	if s.pingErr != nil {
		return nil, s.pingErr
	}
	return &emptypb.Empty{}, nil
}

type connectionStats struct {
	ended chan struct{}
}

func (*connectionStats) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (*connectionStats) HandleRPC(context.Context, stats.RPCStats) {}

func (*connectionStats) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (s *connectionStats) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, ok := event.(*stats.ConnEnd); ok {
		select {
		case s.ended <- struct{}{}:
		default:
		}
	}
}

func TestLoadToolProvidersLoadsMultipleGRPCAndMCPServers(t *testing.T) {
	grpcAddressOne, grpcServiceOne, grpcConnectionOne := startTestPluginServer(t, "grpc_one", nil)
	grpcAddressTwo, grpcServiceTwo, _ := startTestPluginServer(t, "grpc_two", nil)
	mcpServerOne := startTestMCPServer(t, "mcp_one")
	mcpServerTwo := startTestMCPServer(t, "mcp_two")

	cfg := &config.Config{
		Agent: config.Agent{Name: "provider-test"},
		Tools: []config.Tool{
			{Provider: config.ToolProviderGRPC, Address: grpcAddressOne},
			{Provider: config.ToolProviderGRPC, Address: grpcAddressTwo},
			{Provider: config.ToolProviderMCP, Transport: config.MCPTransportStreamableHTTP, URL: mcpServerOne.URL},
			{Provider: config.ToolProviderMCP, Transport: config.MCPTransportStreamableHTTP, URL: mcpServerTwo.URL},
		},
	}
	agent := react.NewAgent(nil, 128, ram.NewRAMContextManager())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resources, err := loadToolProviders(ctx, agent, cfg, emptyFS{})
	if err != nil {
		t.Fatalf("loadToolProviders() error = %v", err)
	}
	if len(resources) != 4 {
		t.Fatalf("len(resources) = %d, want 2 gRPC plugins and 2 MCP clients", len(resources))
	}
	for _, service := range []*testPluginService{grpcServiceOne, grpcServiceTwo} {
		if service.initCalls.Load() != 1 {
			t.Errorf("gRPC Init calls = %d, want 1", service.initCalls.Load())
		}
		if service.pingCalls.Load() != 1 {
			t.Errorf("gRPC Ping calls = %d, want 1", service.pingCalls.Load())
		}
	}

	closeResources(resources)
	waitForConnectionEnd(t, grpcConnectionOne)
}

func TestGRPCProviderRollsBackConnectionsWhenPingFails(t *testing.T) {
	firstAddress, _, firstConnection := startTestPluginServer(t, "first", nil)
	secondAddress, _, secondConnection := startTestPluginServer(t, "second", errors.New("ping failed"))
	agent := react.NewAgent(nil, 128, ram.NewRAMContextManager())

	_, err := (grpcProvider{}).Load(context.Background(), agent, []config.Tool{
		{Provider: config.ToolProviderGRPC, Address: firstAddress},
		{Provider: config.ToolProviderGRPC, Address: secondAddress},
	}, emptyFS{}, "")
	if err == nil {
		t.Fatal("Load() error = nil, want ping error")
	}
	waitForConnectionEnd(t, secondConnection)
	waitForConnectionEnd(t, firstConnection)
}

func TestCheckWritablePath(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritablePath(dir); err != nil {
		t.Fatalf("checkWritablePath(directory) error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".goatc-write-check-*")); err != nil || len(matches) != 0 {
		t.Fatalf("write probes were not cleaned up: %v, %v", matches, err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkWritablePath(file); err != nil {
		t.Fatalf("checkWritablePath(file) error = %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("write check modified file: %q, %v", data, err)
	}
}

func TestExpandValuesUsesRuntimeEnvironment(t *testing.T) {
	t.Setenv("GOATC_TEST_TOKEN", "secret")
	got := expandValues(map[string]string{
		"Authorization": "Bearer ${GOATC_TEST_TOKEN}",
	})
	if got["Authorization"] != "Bearer secret" {
		t.Fatalf("Authorization = %q, want %q", got["Authorization"], "Bearer secret")
	}
}

func startTestPluginServer(t *testing.T, name string, pingErr error) (string, *testPluginService, *connectionStats) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	connection := &connectionStats{ended: make(chan struct{}, 1)}
	server := grpc.NewServer(grpc.StatsHandler(connection))
	service := &testPluginService{name: name, pingErr: pingErr}
	pluginpb.RegisterPluginServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), service, connection
}

func waitForConnectionEnd(t *testing.T, connection *connectionStats) {
	t.Helper()
	select {
	case <-connection.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for gRPC connection to close")
	}
}

func startTestMCPServer(t *testing.T, toolName string) *httptest.Server {
	t.Helper()
	server := mcpserver.NewMCPServer("test-server", "1.0.0", mcpserver.WithToolCapabilities(false))
	server.AddTool(
		mcp.NewTool(toolName, mcp.WithDescription("test tool")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		},
	)
	httpServer := httptest.NewServer(mcpserver.NewStreamableHTTPServer(server))
	t.Cleanup(httpServer.Close)
	return httpServer
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}
