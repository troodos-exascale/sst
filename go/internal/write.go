package internal

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Source struct {
	Name        string `yaml:"name"`
	Upstream    string `yaml:"upstream"`
	Sha256      string `yaml:"sha256,omitempty"`
	Format      string `yaml:"format,omitempty"`
	RetrievedAt string `yaml:"retrieved_at,omitempty"`
}

type ImageYAML struct {
	Image struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version,omitempty"`
		Description string `yaml:"description,omitempty"`
	} `yaml:"image"`

	Sources []Source `yaml:"sources"`
}

func WriteYAML(dir string, y *ImageYAML) error {
	b, err := yaml.Marshal(y)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "image.yaml"), b, 0o644)
}
