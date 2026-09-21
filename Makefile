proto:
	protoc --go_out=agent/toolplugin/pb --go-grpc_out=agent/toolplugin/pb --experimental_allow_proto3_optional agent/toolplugin/pb/*.proto

proto-clean:
	rm -f agent/toolplugin/pb/*.go