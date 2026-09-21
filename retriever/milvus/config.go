package milvus

type MilvusConfig struct {
	MilvusAddress  string
	MilvusUsername string
	MilvusPassword string
}

func NewMilvusConfig(opts ...MilvusConfigOption) *MilvusConfig {
	cfg := &MilvusConfig{
		MilvusAddress: "http://localhost:19530",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

type MilvusConfigOption func(*MilvusConfig)

func WithMilvusAddress(address string) MilvusConfigOption {
	return func(cfg *MilvusConfig) {
		cfg.MilvusAddress = address
	}
}

func WithMilvusUsername(username string) MilvusConfigOption {
	return func(cfg *MilvusConfig) {
		cfg.MilvusUsername = username
	}
}

func WithMilvusPassword(password string) MilvusConfigOption {
	return func(cfg *MilvusConfig) {
		cfg.MilvusPassword = password
	}
}
