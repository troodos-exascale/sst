// File: pkg/sst/manifests.go
package sst

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// --- Generic Manifest Loader ---

// LoadManifest reads a YAML file from the given path and unmarshals it into the
// provided interface value 'v'. 'v' must be a pointer to a struct.
func LoadManifest(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read manifest file %s: %w", path, err)
	}
	// Use yaml.Unmarshal Strict mode if desired later for stricter validation
	if err := yaml.Unmarshal(data, v); err != nil {
		// Provide more context on YAML parsing errors
		var node yaml.Node
		if yaml.Unmarshal(data, &node) == nil { // Try unmarshalling into generic node for line numbers
			// If this succeeds, the structure was wrong for 'v' but YAML was valid-ish
			// We can potentially add line number info here later if needed.
		}
		return fmt.Errorf("could not parse manifest YAML %s: %w", path, err)
	}
	return nil
}

// --- Manifest Struct Definitions (Refined for v7 YAML) ---

// HardwareProbe remains the same
type HardwareProbe struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Timestamp time.Time `yaml:"timestamp"`
	} `yaml:"metadata"`
	Spec struct {
		BlockDevices []BlockDevice `yaml:"blockDevices"`
		// ... other hardware specs like cpu, memory etc.
	} `yaml:"spec"`
}

// BlockDevice remains the same
type BlockDevice struct {
	Name      string `yaml:"name"`
	HostPath  string `yaml:"hostPath"`
	Serial    string `yaml:"serial"`
	SizeBytes uint64 `yaml:"sizeBytes"`
}

// StorageLayout and related structs remain the same
type ResourceStatus struct {
	State   string `yaml:"state"`
	Message string `yaml:"message,omitempty"`
}
type LuksEncryption struct {
	Enable  bool   `yaml:"enable"`
	KeyFile string `yaml:"keyFile"`
}
type Device struct {
	Name       string         `yaml:"name"`
	Kind       string         `yaml:"kind"`
	DeviceRef  string         `yaml:"deviceRef,omitempty"`
	Directory  string         `yaml:"directory,omitempty"`
	Size       string         `yaml:"size,omitempty"`
	Encryption LuksEncryption `yaml:"encryption,omitempty"`
	Status     ResourceStatus `yaml:"status"`
}
type Pool struct {
	Name      string         `yaml:"name"`
	RaidLevel string         `yaml:"raidLevel"`
	Devices   []string       `yaml:"devices"`
	Encrypt   bool           `yaml:"encrypt,omitempty"`
	Status    ResourceStatus `yaml:"status"`
}
type Filesystem struct {
	Name           string         `yaml:"name"`
	Kind           string         `yaml:"kind"`
	DeviceRef      string         `yaml:"deviceRef"`
	PoolRef        string         `yaml:"poolRef,omitempty"`
	Mountpoint     string         `yaml:"mountpoint"`
	MountOptions   []string       `yaml:"mountOptions,omitempty"`
	Subdirectories []string       `yaml:"subdirectories,omitempty"`
	Status         ResourceStatus `yaml:"status"`
}
type StorageLayout struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Spec       struct {
		Devices     []Device     `yaml:"devices"`
		Pools       []Pool       `yaml:"pools"`
		Filesystems []Filesystem `yaml:"filesystems"`
	} `yaml:"spec"`
}

// --- Artifact Build Manifests (Matching v7 YAML) ---

// ArtifactInput defines dependencies and sources for an artifact or build step rule.
type ArtifactInput struct {
	From          string            `yaml:"from"` // Refers to another artifact ID for base image
	RequiresPkgs  []string          `yaml:"requires_pkgs"`
	Files         []string          `yaml:"files"`         // Source files for this artifact/step
	BaseFormatTag string            `yaml:"base_format_tag"` // For multi-step packages
	// RuleArgs holds arguments specific to the build rule being used.
	RuleArgs map[string]string `yaml:"args,omitempty"` // Arguments passed to the rule's script
}

// ArtifactBuildStep defines a single action in a multi-step build (primarily for packages).
type ArtifactBuildStep struct {
	Rule    string          `yaml:"rule"`   // Name of the build rule to use
	Inputs  ArtifactInput   `yaml:"inputs"` // Inputs specific to this step (overrides top-level inputs?) - No, inputs are usually for the overall artifact. Args are per-step.
	Args    map[string]string `yaml:"args,omitempty"` // Specific args for THIS step's rule invocation
	Outputs []ArtifactOutput `yaml:"outputs"`
}

// ArtifactOutput defines a potential result of a build step or artifact.
type ArtifactOutput struct {
	Format    string `yaml:"format"`     // e.g., container, qcow2, deb
	FormatTag string `yaml:"format_tag"` // e.g., upstream_binary (for intermediate steps)
}

// ArtifactDefinition describes a single buildable artifact (package or runtime).
type ArtifactDefinition struct {
	Kind        string              `yaml:"kind"` // "pkg", "runtime", "utility"
	Description string              `yaml:"description"`
	Arch        []string            `yaml:"arch"`
	Version     string              `yaml:"version,omitempty"` // Primarily for packages
	Formats     []string            `yaml:"formats,omitempty"` // Final output formats for runtimes/utilities
	BuildSteps  []ArtifactBuildStep `yaml:"build_actions,omitempty"` // Renamed from build_steps for clarity with pkg focus
	Inputs      ArtifactInput       `yaml:"inputs,omitempty"`    // Common inputs for single-step runtimes/utilities or default inputs for pkgs
	SourceImage string              `yaml:"source_image,omitempty"`// For utility images like 'registry'
	BuildRule   string              `yaml:"build_rule,omitempty"` // Optional: Override rule for special cases (e.g., HPC conversion)
}

// BuildRule defines how to execute a specific build action.
type BuildRule struct {
	Description   string   `yaml:"description"`
	Script        string   `yaml:"script"`         // Path relative to images/
	BuildSequence []string `yaml:"build_sequence"` // Explicit prerequisite *tasks* within this rule
	ImplicitDeps  []string `yaml:"implicit_deps"`  // Meta deps (service ready) or implicit Artifact deps (build env container)
}

// MetaDependency defines non-artifact prerequisites like running services.
type MetaDependency struct {
	Description string `yaml:"description"`
	ArtifactID  string `yaml:"artifact_id"` // Points to an artifact definition
	Format      string `yaml:"format"`      // Required format of the artifact
	CheckScript string `yaml:"check_script"`// Script to ensure it's ready (e.g., start-registry)
}

// ArtifactsManifest is the top-level structure for images/artifacts.yaml.
type ArtifactsManifest struct {
	BuildRules map[string]BuildRule          `yaml:"build_rules"` // Key is rule name
	MetaDeps   map[string]MetaDependency     `yaml:"meta_deps"`   // Key is meta dep name
	Artifacts  map[string]ArtifactDefinition `yaml:"artifacts"`   // Key is artifact ID
}
