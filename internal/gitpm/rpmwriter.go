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
	"os"
	"time"

	"github.com/goreleaser/nfpm/v2"
	"github.com/goreleaser/nfpm/v2/files" // <-- add this
	_ "github.com/goreleaser/nfpm/v2/rpm"
)

// BuildRPM creates a deterministic .rpm package using nFPM.
func BuildRPM(opts *Options) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}

	packager, err := nfpm.Get("rpm")
	if err != nil {
		return "", fmt.Errorf("rpm: could not get packager: %w", err)
	}

	entries, err := FSWalk(opts.Chdir, opts.Prefix, true)
	if err != nil {
		return "", fmt.Errorf("rpm: fswalk failed: %w", err)
	}

	// nFPM expects a slice of *files.Content, not a map, and under Overridables.
	var contents files.Contents

	for _, e := range entries {
		dst := e.Rel

		c := &files.Content{
			Destination: dst,
			FileInfo: &files.ContentFileInfo{
				Owner: "root",
				Group: "root",
				Mode:  e.Mode,
				// deterministic timestamp
				MTime: time.Unix(0, 0),
			},
		}

		switch e.Type {
		case NodeFile:
			c.Type = files.TypeFile
			c.Source = e.Abs
		case NodeDir:
			c.Type = files.TypeDir
		case NodeSymlink:
			// In nFPM, for symlink entries: Destination = link path, Source = target
			c.Type = files.TypeSymlink
			c.Source = e.Linkname
		case NodeHardlink:
			// nFPM does not have an explicit “hardlink” content type in v2.
			// If you need true hardlink preservation, we’ll need a different approach.
			// For now, skip or treat as file as appropriate for your use-case.
			continue
		default:
			continue
		}

		contents = append(contents, c)
	}

	info := &nfpm.Info{
		Name:        opts.Name,
		Version:     opts.Version,
		Arch:        opts.Arch,
		Maintainer:  opts.Maintainer,
		Description: opts.Description,
		License:     opts.License,

		// Deterministic file mtimes for all packaged entries:
		MTime: time.Unix(0, 0),

		Overridables: nfpm.Overridables{
			Contents: contents, // <= lives here as files.Contents
		},
	}

	out := fmt.Sprintf("%s_%s_%s.rpm", opts.Name, opts.Version, opts.Arch)
	f, err := os.Create(out)
	if err != nil {
		return "", fmt.Errorf("rpm: could not create package file: %w", err)
	}
	defer f.Close()

	if err := packager.Package(info, f); err != nil {
		return "", fmt.Errorf("rpm: package creation failed: %w", err)
	}

	fmt.Printf("✅ built %s\n", out)
	return out, nil
}
