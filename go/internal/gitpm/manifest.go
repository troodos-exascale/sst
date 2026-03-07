package bpmpkg

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// v1.3 minimal schema (arch omitted; taken from runtime)
type Manifest struct {
	Schema  string `yaml:"schema"`
	Kind    string `yaml:"kind"` // "package" default; future: oci|initrd|vm
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	Repo    string `yaml:"repo"`

	Source struct {
		Method string `yaml:"method"` // git|archive|local|exec
		Tag    string `yaml:"tag"`    // for git
		Signed bool   `yaml:"signed"` // ignored for now
		URL    string `yaml:"url"`    // for archive
		SHA256 string `yaml:"sha256"`
		Fetch  string `yaml:"fetch"` // executable command (mutually exclusive with URL)
	} `yaml:"source"`

	Build struct {
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"build"`

	Install struct {
		Prefix  string   `yaml:"prefix"`
		Include []string `yaml:"include"`
		Exclude []string `yaml:"exclude"`
	} `yaml:"install"`
}

// LoadManifest reads and parses one YAML manifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Kind == "" {
		m.Kind = "package"
	}
	if m.Name == "" || m.Version == "" {
		return nil, fmt.Errorf("manifest %s missing name or version", filepath.Base(path))
	}
	return &m, nil
}
