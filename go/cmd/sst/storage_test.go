package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"gopkg.in/yaml.v3"
)

// setupTestEnvironment initializes the sst.Core and returns a logger.
// This is necessary because the main rootCmd's PersistentPreRunE, which normally
// initializes the 'core' variable, does not run in this test context.
func setupTestEnvironment(t *testing.T) sst.Logx {
	t.Helper()
	var err error
	// HACK: To allow sst.New() to work outside a git repo, we temporarily
	// create a fake .git directory.
	tmpGitDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.Mkdir(tmpGitDir, 0755))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(filepath.Dir(tmpGitDir)))
	defer func() {
		require.NoError(t, os.Chdir(originalWD))
	}()

	core, err = sst.New()
	require.NoError(t, err)

	return core.Logx().Subsystem("storage-test")
}

func TestComposeStoragePlan(t *testing.T) {
	// Setup the test environment and logger
	_ = setupTestEnvironment(t)

	// 1. Create a temporary workspace directory for the test.
	testDir := t.TempDir()
	originalWD, err := os.Getwd()
	require.NoError(t, err, "Failed to get current working directory")
	require.NoError(t, os.Chdir(testDir), "Failed to change to temp directory")
	defer func() {
		require.NoError(t, os.Chdir(originalWD), "Failed to restore original working directory")
	}()

	// 2. Create the mock hardware.yaml input file.
	mockProbe := sst.HardwareProbe{
		APIVersion: "sst.io/v1",
		Kind:       "HardwareProbe",
		Spec: struct {
			BlockDevices []sst.BlockDevice `yaml:"blockDevices"`
		}{
			BlockDevices: []sst.BlockDevice{
				{Name: "nvme0n1", HostPath: "/dev/disk/by-id/nvme-eui.001", Serial: "SERIAL1", SizeBytes: 1000 * 1024 * 1024 * 1024},
				{Name: "nvme1n1", HostPath: "/dev/disk/by-id/nvme-eui.002", Serial: "SERIAL2", SizeBytes: 1000 * 1024 * 1024 * 1024},
			},
		},
	}
	probeBytes, err := yaml.Marshal(&mockProbe)
	require.NoError(t, err, "Failed to marshal mock probe")
	hardwareFile := "hardware.yaml"
	require.NoError(t, os.WriteFile(hardwareFile, probeBytes, 0644), "Failed to write mock hardware.yaml")

	// 3. Execute the command programmatically.
	// We create a temporary root command and attach only the command we want to test.
	testRootCmd := &cobra.Command{Use: "sst-test", SilenceErrors: true, SilenceUsage: true}
	testRootCmd.AddCommand(composeStoragePlanCmd)

	outputFile := "my-storage-plan.yaml"
	args := []string{
		"compose-storageplan",
		"--node", hardwareFile,
		"--remainder", "data-pool,mirror",
		"--filesets", "postgres,minio",
		"--output", outputFile,
	}
	testRootCmd.SetArgs(args)

	err = testRootCmd.Execute()
	require.NoError(t, err, "Command execution failed")

	// 4. Assert the results.
	// Check that the output file was created.
	_, err = os.Stat(outputFile)
	require.NoError(t, err, "Expected output file was not created")

	// Load and verify the contents of the generated layout.
	var generatedLayout sst.StorageLayout
	err = sst.LoadManifest(outputFile, &generatedLayout)
	require.NoError(t, err, "Failed to load generated storage-layout.yaml")

	// Assertions for the generated plan.
	assert.Equal(t, "StorageLayout", generatedLayout.Kind, "Kind should be StorageLayout")
	require.Len(t, generatedLayout.Spec.Devices, 2, "Should have generated 2 device entries")
	require.Len(t, generatedLayout.Spec.Pools, 1, "Should have generated 1 pool entry")
	require.Len(t, generatedLayout.Spec.Filesystems, 2, "Should have generated 2 fileset entries")

	// Check device details
	assert.Equal(t, "disk0", generatedLayout.Spec.Devices[0].Name)
	assert.Equal(t, "/dev/disk/by-id/nvme-eui.001", generatedLayout.Spec.Devices[0].DeviceRef)
	assert.Equal(t, "plan-recorded", generatedLayout.Spec.Devices[0].Status.State, "Device status should be 'plan-recorded'")

	assert.Equal(t, "disk1", generatedLayout.Spec.Devices[1].Name)
	assert.Equal(t, "/dev/disk/by-id/nvme-eui.002", generatedLayout.Spec.Devices[1].DeviceRef)
	assert.Equal(t, "plan-recorded", generatedLayout.Spec.Devices[1].Status.State, "Device status should be 'plan-recorded'")

	// Check pool details
	pool := generatedLayout.Spec.Pools[0]
	assert.Equal(t, "data-pool", pool.Name)
	assert.Equal(t, "mirror", pool.RaidLevel)
	assert.Equal(t, "plan-recorded", pool.Status.State, "Pool status should be 'plan-recorded'")
	assert.Contains(t, pool.Devices, "disk0")
	assert.Contains(t, pool.Devices, "disk1")

	// Check fileset details
	fs1 := generatedLayout.Spec.Filesystems[0]
	assert.Equal(t, "postgres-data", fs1.Name)
	assert.Equal(t, "zfs-fileset", fs1.Kind)
	assert.Equal(t, "data-pool", fs1.PoolRef)
	assert.Equal(t, "/mnt/data/postgres", fs1.Mountpoint)
	assert.Equal(t, "plan-recorded", fs1.Status.State, "Fileset status should be 'plan-recorded'")

	fs2 := generatedLayout.Spec.Filesystems[1]
	assert.Equal(t, "minio-data", fs2.Name)
	assert.Equal(t, "plan-recorded", fs2.Status.State, "Fileset status should be 'plan-recorded'")
}
