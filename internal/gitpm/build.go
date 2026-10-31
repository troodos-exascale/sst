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
	"fmt"
)

// BuildOptions mirrors CLI flags for constructing packages.
type BuildOptions struct {
	SourceType  string
	TargetType  string
	Name        string
	Version     string
	Prefix      string
	BuildDir    string
	Description string
}

// Build dispatches to the appropriate packager (deb, rpm, etc.).
func Build(opts BuildOptions) error {
	switch opts.TargetType {
	case "deb":
		o := &Options{
			InputType:   opts.SourceType,
			OutputType:  opts.TargetType,
			Name:        opts.Name,
			Version:     opts.Version,
			Chdir:       opts.BuildDir,
			Description: opts.Description,
		}
		out, err := BuildDeb(o)
		if err != nil {
			return fmt.Errorf("build failed: %v", err)
		}
		fmt.Printf("✅ built %s\n", out)
		return nil

	default:
		return fmt.Errorf("unsupported target type: %s", opts.TargetType)
	}
}
