package gitpm

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BuildDir string `yaml:"build_dir"`
	DistDir  string `yaml:"dist_dir"`
	CloneDir string `yaml:"clone_dir"`
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		BuildDir: "buildroot",
		DistDir:  "dist",
		CloneDir: ".cache/gitpm",
	}

	home, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(home, ".config", "gitpm", "config.yaml")
		data, err := os.ReadFile(configPath)
		if err == nil {
			_ = yaml.Unmarshal(data, cfg)
		}
	}

	// Environment overrides for hermetic E2E tests
	if env := os.Getenv("SST_PACKAGE_BUILD_DIR"); env != "" {
		cfg.BuildDir = env
	}
	if env := os.Getenv("SST_PACKAGE_DIST_DIR"); env != "" {
		cfg.DistDir = env
	}

	return cfg, nil
}
