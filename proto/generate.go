// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/gitpm/v1/manifest.proto
//go:generate protoc -I. --go_out=. --go_opt=paths=source_relative yaml/compose/v1/compose.proto
