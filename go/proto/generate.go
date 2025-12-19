package proto

// -----------------------------------------------------------------------------
// gRPC / API protos (EXISTING – must not regress)
// -----------------------------------------------------------------------------

//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative external/v1/hi_core.proto

// -----------------------------------------------------------------------------
// YAML-backed schemas (non-gRPC, versioned)
// -----------------------------------------------------------------------------

//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/hardware/v1/hwprobe.proto
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/storage/v1/storage.proto
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/runctl/v1/instance.proto
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/mkimage/v1/image.proto
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/compose/v1/compose.proto
