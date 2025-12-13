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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

var (
	orderArg   string
	clusterArg string
)

// YAML structures for parsing manifests
type orderYAML struct {
	Host struct {
		SshKeyName string `yaml:"sshKeyName"`
	} `yaml:"host"`
}
type projectYAML struct {
	SecretsManagement struct {
		Provider string `yaml:"provider"`
		Path     string `yaml:"path"`
		Secrets  []struct {
			Name   string `yaml:"name"`
			Source string `yaml:"source"`
		} `yaml:"secrets"`
	} `yaml:"secretsManagement"`
}

var probeCmd = &cobra.Command{
	Use:   "probe [--order <order_path> | --cluster <cluster_name>]",
	Short: "Probes a target host based on an order or cluster manifest.",
	Long: `Orchestrates a hardware probe by leveraging the 'active-project' context.
It uses the relative path from --order or the name from --cluster to find the
target manifest within the active project's 'orders' or 'clusters' directory.`,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Lstat("active-project"); os.IsNotExist(err) {
			return fmt.Errorf("no active project set. Please run 'sst project <path>' first")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		runner := core.Steps("sst-probe-client")
		state := make(map[string]interface{})
		log := core.Logx().Subsystem("sst-probe")

		ctx, end := sst.StartSpan(context.Background(), "sst-probe")
		defer end(nil)
		state["ctx"] = ctx
		state["log"] = log

		runner.SetInitialState(state)

		runner.AddStep("Resolve Paths and Read Config", stepResolveAndRead)
		runner.AddStep("Establish gRPC Connection", stepConnectGrpc)
		runner.AddStep("Call Probe RPC", stepCallProbe)
		runner.AddStep("Call Normalize RPC", stepCallNormalize)
		// RENAMED: Changed stepWriteOutput to stepWriteProbeOutput
		runner.AddStep("Write Output Manifest", stepWriteProbeOutput)

		// CORRECTED: Check error on conn.Close().
		defer func() {
			if conn, ok := state["grpc_conn"].(*grpc.ClientConn); ok {
				if err := conn.Close(); err != nil {
					log.WithError(err).Warn(ctx, "Failed to close gRPC connection cleanly")
				}
			}
		}()

		log.Info(ctx, "Starting hardware probe workflow...")
		err := runner.Run(0, 0, 0)
		if err != nil {
			log.WithError(err).Error(ctx, "Hardware probe workflow failed")
			return err
		}
		log.Info(ctx, "Hardware probe workflow completed successfully.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(probeCmd)
	probeCmd.Flags().StringVar(&orderArg, "order", "", "Path to order directory relative to <project>/orders/")
	probeCmd.Flags().StringVar(&clusterArg, "cluster", "", "Name of cluster in <project>/clusters/")
	probeCmd.MarkFlagsMutuallyExclusive("order", "cluster")
}

// --- Step Functions for the sst Probe Client ---

func stepResolveAndRead(s map[string]interface{}) error {
	log := s["log"].(sst.Logx)
	ctx := s["ctx"].(context.Context)
	repoRoot := core.Files().Root()

	// 1. Resolve active project root from the symlink.
	projectRoot, err := os.Readlink("active-project")
	if err != nil {
		return fmt.Errorf("could not read 'active-project' symlink: %w", err)
	}
	s["projectRoot"] = projectRoot

	// 2. Determine the full path to the manifest (order or cluster)
	var manifestPath string
	if orderArg != "" {
		manifestPath = filepath.Join(projectRoot, "orders", orderArg, "order.yaml")
	} else if clusterArg != "" {
		// CORRECTED: Removed ineffectual assignment as this feature is not yet implemented.
		return status.Errorf(codes.Unimplemented, "probing based on --cluster is not yet implemented")
	} else {
		return fmt.Errorf("either --order or --cluster must be specified")
	}
	s["manifestPath"] = manifestPath

	// 3. Load project and order manifests
	projectManifestPath := filepath.Join(projectRoot, "manifests", "project.yaml")
	log.Info(ctx, "Loading project configuration", "path", projectManifestPath)

	projectBytes, err := os.ReadFile(projectManifestPath)
	if err != nil {
		return fmt.Errorf("could not read project manifest at '%s': %w", projectManifestPath, err)
	}
	var project projectYAML
	if err := yaml.Unmarshal(projectBytes, &project); err != nil {
		return fmt.Errorf("failed to parse project YAML '%s': %w", projectManifestPath, err)
	}

	orderBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read order file '%s': %w", manifestPath, err)
	}
	var order orderYAML
	if err := yaml.Unmarshal(orderBytes, &order); err != nil {
		return fmt.Errorf("failed to parse order YAML '%s': %w", manifestPath, err)
	}
	if order.Host.SshKeyName == "" {
		return fmt.Errorf("host.sshKeyName not found in order file '%s'", manifestPath)
	}

	// 4. Resolve the secret content
	var sshKeyBytes []byte
	provider := project.SecretsManagement.Provider
	log.Info(ctx, "Resolving secret", "key_name", order.Host.SshKeyName, "provider", provider)

	switch provider {
	case "localFile":
		secretMap := make(map[string]string)
		for _, secret := range project.SecretsManagement.Secrets {
			secretMap[secret.Name] = secret.Source
		}

		sourceFileName, ok := secretMap[order.Host.SshKeyName]
		if !ok {
			return fmt.Errorf("secret with name '%s' is not defined in project.yaml's secretsManagement list", order.Host.SshKeyName)
		}

		secretsBasePath := project.SecretsManagement.Path
		if secretsBasePath == "" {
			return fmt.Errorf("secretsManagement.path is required for localFile provider")
		}
		keyPath := filepath.Join(repoRoot, secretsBasePath, sourceFileName)
		sshKeyBytes, err = os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("failed to read SSH key file '%s': %w", keyPath, err)
		}
	case "vault":
		return status.Errorf(codes.Unimplemented, "secret provider 'vault' is not yet implemented")
	default:
		return fmt.Errorf("unknown secret provider in project.yaml: '%s'", provider)
	}

	s["order_content"] = orderBytes
	s["ssh_key_content"] = sshKeyBytes
	return nil
}

func stepConnectGrpc(s map[string]interface{}) error {
		pluginAddress := "localhost:50051"

	interceptor := sst.GRPCClientUnaryInterceptor()

	// CORRECTED: Use grpc.NewClient instead of the deprecated grpc.DialContext.
	// The context for timeout is handled by the transport credentials layer.
	conn, err := grpc.NewClient(pluginAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(interceptor),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to plugin at %s: %w", pluginAddress, err)
	}
	s["grpc_conn"] = conn
	return nil
}

func stepCallProbe(s map[string]interface{}) error {
	ctx := s["ctx"].(context.Context)
	conn := s["grpc_conn"].(*grpc.ClientConn)
	client := pb.NewSstHiCoreServiceClient(conn)

	req := &pb.ProbeRequest{
		OrderYamlContent: s["order_content"].([]byte),
		SshPrivateKey:    s["ssh_key_content"].([]byte),
	}

	res, err := client.Probe(ctx, req)
	if err != nil {
		return fmt.Errorf("probe RPC failed: %w", err)
	}
	s["probe_session_id"] = res.GetProbeSessionId()
	return nil
}

func stepCallNormalize(s map[string]interface{}) error {
	ctx := s["ctx"].(context.Context)
	conn := s["grpc_conn"].(*grpc.ClientConn)
	client := pb.NewSstHiCoreServiceClient(conn)

	req := &pb.NormalizeRequest{
		ProbeSessionId: s["probe_session_id"].(string),
	}

	res, err := client.NormalizeProbeData(ctx, req)
	if err != nil {
		return fmt.Errorf("normalize RPC failed: %w", err)
	}
	s["normalized_yaml"] = res.GetNormalizedYamlContent()
	return nil
}

// RENAMED: Changed stepWriteOutput to stepWriteProbeOutput
func stepWriteProbeOutput(s map[string]interface{}) error {
	logger := s["log"].(sst.Logx)
	ctx := s["ctx"].(context.Context)
	yamlBytes := s["normalized_yaml"].([]byte)

	projectRoot, ok := s["projectRoot"].(string)
	if !ok || projectRoot == "" {
		return fmt.Errorf("projectRoot not found in workflow state")
	}

	var data struct {
		Metadata struct {
			Identifiers struct {
				SerialNumber string `yaml:"serialNumber"`
			} `yaml:"identifiers"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(yamlBytes, &data); err != nil {
		return fmt.Errorf("could not parse normalized YAML to determine filename: %w", err)
	}

	id := data.Metadata.Identifiers.SerialNumber
	if id == "" {
		return fmt.Errorf("normalized YAML is missing metadata.identifiers.serialNumber")
	}

	filename := fmt.Sprintf("%s.hardware.yaml", id)
	deliveriesDir := filepath.Join(projectRoot, "deliveries")
	outputPath := filepath.Join(deliveriesDir, filename)

	if err := os.MkdirAll(deliveriesDir, 0755); err != nil {
		return fmt.Errorf("failed to create deliveries directory '%s': %w", deliveriesDir, err)
	}

	logger.Info(ctx, "Writing normalized hardware manifest", "path", outputPath)
	return os.WriteFile(outputPath, yamlBytes, 0644)
}
