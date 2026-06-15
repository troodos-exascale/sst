package server

import (
	"context"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	sst "github.com/troodos-exascale/sst/internal/sst"
	pb "github.com/troodos-exascale/sst/proto/external/v1"

	"github.com/troodos-exascale/sst/cmd/hetzner-plugin/internal/probe"
)

// Server implements the gRPC service.
type Server struct {
	pb.UnimplementedSstHiCoreServiceServer
	log sst.Logx
}

// New creates a new server instance.
func New(log *sst.Logx) *Server {
	return &Server{
		log: *log.Subsystem("plugin-server"),
	}
}

// Probe executes the hardware discovery process on a remote machine.
func (s *Server) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	s.log.Info(ctx, "Probe RPC called")

	tempDir, err := os.MkdirTemp("", "sst-probe-*")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create temp dir: %v", err)
	}
	s.log.Info(ctx, "Created temp directory for probe data", "path", tempDir)

	var orderData struct {
		Host struct {
			Hostname string `yaml:"hostname"`
		} `yaml:"host"`
	}
	if err := yaml.Unmarshal(req.GetOrderYamlContent(), &orderData); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse order YAML: %v", err)
	}
	hostname := orderData.Host.Hostname
	if hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname not found in order YAML")
	}

	if err := probe.RunRemoteProbe(ctx, &s.log, hostname, req.GetSshPrivateKey(), tempDir); err != nil {
		return nil, status.Errorf(codes.Internal, "remote probe failed: %v", err)
	}

	return &pb.ProbeResponse{ProbeSessionId: tempDir}, nil
}

// NormalizeProbeData takes the handle from a completed probe and returns the normalized manifest.
func (s *Server) NormalizeProbeData(ctx context.Context, req *pb.NormalizeRequest) (*pb.NormalizeResponse, error) {
	s.log.Info(ctx, "NormalizeProbeData RPC called", "session_id", req.GetProbeSessionId())

	yamlBytes, err := probe.Normalize(ctx, &s.log, req.GetProbeSessionId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to normalize probe data: %v", err)
	}

	return &pb.NormalizeResponse{NormalizedYamlContent: yamlBytes}, nil
}

// RegisterService registers the server implementation with a gRPC server.
func RegisterService(g *grpc.Server, log *sst.Logx) {
	pb.RegisterSstHiCoreServiceServer(g, New(log))
}
