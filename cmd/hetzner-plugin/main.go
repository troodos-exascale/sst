package main

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/troodos-exascale/sst/cmd/hetzner-plugin/internal/server"
	sst "github.com/troodos-exascale/sst/internal/sst"
)

const port = ":50051"

func main() {
	// Construct explicit root logger
	log := sst.LogxRoot().Subsystem("plugin-main")
	ctx := context.Background()

	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.WithError(err).Error(ctx, "failed to listen on port", "port", port)
		sst.SafeExit(ctx, log, 1)
		return
	}

	s := grpc.NewServer()

	// Inject only what the server needs
	server.RegisterService(s, log)

	reflection.Register(s)

	log.Info(ctx, "gRPC server starting", "address", lis.Addr().String())

	if err := s.Serve(lis); err != nil {
		log.WithError(err).Error(ctx, "gRPC server failed to serve")
		sst.SafeExit(ctx, log, 1)
	}
}
