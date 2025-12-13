package main

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"github.com/swiss-software-tool/runtime-smart/go/cmd/hetzner-plugin/internal/server"
	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

const port = ":50051"

func main() {
	core, err := sst.New()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize sst core package: %v", err))
	}
	log := core.Logx().Subsystem("plugind-main")
	ctx := context.Background()

	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.WithError(err).Error(ctx, "failed to listen on port", "port", port)
		sst.SafeExit(ctx, log, 1)
		return
	}

	s := grpc.NewServer()

	server.RegisterService(s, core)

	reflection.Register(s)

	log.Info(ctx, "gRPC server starting", "address", lis.Addr().String())

	if err := s.Serve(lis); err != nil {
		log.WithError(err).Error(ctx, "gRPC server failed to serve")
		sst.SafeExit(ctx, log, 1)
	}
}
