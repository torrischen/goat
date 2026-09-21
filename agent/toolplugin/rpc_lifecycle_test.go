package toolplugin

import (
	"context"
	"net"
	"testing"

	"github.com/torrischen/goat/agent/toolplugin/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/protobuf/types/known/emptypb"
)

type lifecyclePluginService struct {
	pb.UnimplementedPluginServiceServer
}

func (lifecyclePluginService) Init(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (lifecyclePluginService) Name(context.Context, *emptypb.Empty) (*pb.NameResponse, error) {
	return &pb.NameResponse{Name: "lifecycle"}, nil
}

func (lifecyclePluginService) Description(context.Context, *emptypb.Empty) (*pb.DescriptionResponse, error) {
	return &pb.DescriptionResponse{Description: "lifecycle test"}, nil
}

func (lifecyclePluginService) Properties(context.Context, *emptypb.Empty) (*pb.PropertiesResponse, error) {
	return &pb.PropertiesResponse{}, nil
}

func TestRPCPluginCloseClosesConnection(t *testing.T) {
	address := startLifecyclePluginServer(t)
	plugin, closer, err := LoadPluginsFromRPC(address)
	if err != nil {
		t.Fatalf("LoadPluginsFromRPC() error = %v", err)
	}

	rpcPlugin, ok := plugin.(*rpcToolPlugin)
	if !ok {
		t.Fatalf("plugin type = %T, want *rpcToolPlugin", plugin)
	}

	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := rpcPlugin.conn.GetState(); got != connectivity.Shutdown {
		t.Fatalf("connection state = %s, want %s", got, connectivity.Shutdown)
	}
}

func startLifecyclePluginServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	pb.RegisterPluginServiceServer(server, lifecyclePluginService{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}
