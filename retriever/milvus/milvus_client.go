package milvus

import (
	"context"
	"time"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"
)

func NewMilvusClient(ctx context.Context, mcfg *MilvusConfig) (*milvusclient.Client, error) {
	milvusClient, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  mcfg.MilvusAddress,
		Username: mcfg.MilvusUsername,
		Password: mcfg.MilvusPassword,
		DialOptions: []grpc.DialOption{
			grpc.WithBlock(),
			grpc.WithTimeout(3 * time.Second),
		},
	})
	if err != nil {
		return nil, err
	}

	return milvusClient, nil
}
