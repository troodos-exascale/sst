// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
