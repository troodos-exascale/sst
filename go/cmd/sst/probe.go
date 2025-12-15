package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	pb "github.com/swiss-software-tool/runtime-smart/go/proto/external/v1"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/codes"
	"gopkg.in/yaml.v3"
)

var (
	orderArg   string
	clusterArg string
)

//
// =========================
// Application root
// =========================

type probeApp struct {
	// execution context
	ctx context.Context
	log sst.Logx

	// resolved paths & data
	repoRoot     string
	projectRoot  string
	manifestPath string

	orderContent []byte
	sshKey       []byte

	// grpc
	conn *grpc.ClientConn

	// behavior + framework
	methods probeMethods
	steps   *sst.StepsRunState
}

//
// =========================
// Application methods table
// =========================

type probeMethods struct {
	ResolveAndRead func(*probeApp) error
	ConnectGrpc    func(*probeApp) error
	CallProbe      func(*probeApp) error
	CallNormalize  func(*probeApp) error
}

//
// =========================
// CLI wiring
// =========================

var probeCmd = &cobra.Command{
	Use:   "probe [--order <order_path> | --cluster <cluster_name>]",
	Short: "Probes a target host based on an order or cluster manifest.",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Lstat("active-project"); os.IsNotExist(err) {
			return fmt.Errorf("no active project set. Please run 'sst project <path>' first")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {

		ctx, end := sst.StartSpan(context.Background(), "sst-probe")
		defer end(nil)

		app := &probeApp{
			ctx: ctx,
			log: sst.LogxRoot().Subsystem("sst-probe"),
		}

		app.methods = probeMethods{
			ResolveAndRead: probeResolveAndRead,
			ConnectGrpc:    probeConnectGrpc,
			CallProbe:      probeCallProbe,
			CallNormalize:  probeCallNormalize,
		}

		app.steps = sst.StepsInit("sst-probe-client")
		app.steps.StepsWithLogger(func(f string, a ...any) {
			app.log.Info(app.ctx, f, a...)
		})

		app.steps.StepsAdd("resolve and read config", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ResolveAndRead(app)
		})
		app.steps.StepsAdd("connect grpc", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ConnectGrpc(app)
		})
		app.steps.StepsAdd("call probe rpc", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.CallProbe(app)
		})
		app.steps.StepsAdd("call normalize rpc", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.CallNormalize(app)
		})

		err := sst.StepsRun(app.steps)
		if app.conn != nil {
			_ = app.conn.Close()
		}
		return err
	},
}

func init() {
	rootCmd.AddCommand(probeCmd)
	probeCmd.Flags().StringVar(&orderArg, "order", "", "Path to order directory")
	probeCmd.Flags().StringVar(&clusterArg, "cluster", "", "Cluster name")
	probeCmd.MarkFlagsMutuallyExclusive("order", "cluster")
}

//
// =========================
// Method implementations
// =========================

func probeResolveAndRead(app *probeApp) error {
	repoRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	app.repoRoot = repoRoot

	projectRoot, err := os.Readlink("active-project")
	if err != nil {
		return err
	}
	app.projectRoot = projectRoot

	if orderArg != "" {
		app.manifestPath = filepath.Join(projectRoot, "orders", orderArg, "order.yaml")
	} else if clusterArg != "" {
		return status.Errorf(codes.Unimplemented, "cluster probing not implemented")
	} else {
		return fmt.Errorf("either --order or --cluster must be specified")
	}

	orderBytes, err := os.ReadFile(app.manifestPath)
	if err != nil {
		return err
	}
	app.orderContent = orderBytes

	var order struct {
		Host struct {
			SshKeyName string `yaml:"sshKeyName"`
		} `yaml:"host"`
	}
	if err := yaml.Unmarshal(orderBytes, &order); err != nil {
		return err
	}
	if order.Host.SshKeyName == "" {
		return fmt.Errorf("host.sshKeyName missing")
	}

	keyPath := filepath.Join(projectRoot, "secrets", order.Host.SshKeyName)
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	app.sshKey = keyBytes
	return nil
}

func probeConnectGrpc(app *probeApp) error {
	conn, err := grpc.NewClient(
		"localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	app.conn = conn
	return nil
}

func probeCallProbe(app *probeApp) error {
	client := pb.NewSstHiCoreServiceClient(app.conn)
	_, err := client.Probe(app.ctx, &pb.ProbeRequest{
		OrderYamlContent: app.orderContent,
		SshPrivateKey:    app.sshKey,
	})
	return err
}

func probeCallNormalize(app *probeApp) error {
	client := pb.NewSstHiCoreServiceClient(app.conn)
	_, err := client.NormalizeProbeData(app.ctx, &pb.NormalizeRequest{})
	return err
}
