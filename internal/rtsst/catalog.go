package rtsst

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/swiss-software-tool/sst/internal/sst"
	mkimagepb "github.com/swiss-software-tool/sst/proto/yaml/mkimage/v1"
	runctlpb "github.com/swiss-software-tool/sst/proto/yaml/runctl/v1"
)

// UpdateCatalog safely links a target directory into ~/.runtimes/<kind>/<name>.
// If the name is already taken by a VALID directory elsewhere, it appends the disambiguator.
func UpdateCatalog(ctx context.Context, log *sst.Logx, kind, name, targetDir, disambiguator string) error {
	catalogDir := filepath.Join(RuntimeDir(), kind)
	if err := os.MkdirAll(catalogDir, 0755); err != nil {
		return fmt.Errorf("failed to create catalog dir: %w", err)
	}

	linkPath := filepath.Join(catalogDir, name)
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}

	// NEW: If the artifact is already physically sitting at its catalog path, do nothing!
	if absTarget == linkPath {
		return nil
	}

	info, err := os.Lstat(linkPath)
	if err == nil {
		// Entry exists
		if info.Mode()&os.ModeSymlink != 0 {
			existingTarget, _ := os.Readlink(linkPath)
			if existingTarget == absTarget {
				return nil // Already perfectly aligned
			}

			// Check if the existing target is dangling (deleted workspace)
			if _, statErr := os.Stat(existingTarget); os.IsNotExist(statErr) {
				log.Info(ctx, "repairing dangling catalog symlink", "kind", kind, "name", name)
				_ = os.Remove(linkPath)
			} else {
				// COLLISION! The symlink points to a different, valid workspace.
				log.Warn(ctx, "NAME COLLISION IN CATALOG: active symlink points elsewhere",
					"name", name, "existing_target", existingTarget, "new_target", absTarget)

				newName := fmt.Sprintf("%s-%s", name, disambiguator)
				linkPath = filepath.Join(catalogDir, newName)
				log.Warn(ctx, "creating disambiguated catalog entry instead", "new_name", newName)
			}
		} else {
			// It's a real directory (legacy/weird state). Leave it alone and disambiguate.
			newName := fmt.Sprintf("%s-%s", name, disambiguator)
			linkPath = filepath.Join(catalogDir, newName)
		}
	}

	// Ensure we don't trip over a previous disambiguated link that is now dangling
	_ = os.Remove(linkPath)

	log.Info(ctx, "catalog.linked", "kind", kind, "link", filepath.Base(linkPath), "target", absTarget)
	return os.Symlink(absTarget, linkPath)
}

// SyncCatalog scans a given workspace directory for images/ and instances/ and aligns them globally.
func SyncCatalog(ctx context.Context, log *sst.Logx, workspaceDir string) {
	// 1. Sync Images
	imagesDir := filepath.Join(workspaceDir, "images")
	if entries, err := os.ReadDir(imagesDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			imgDir := filepath.Join(imagesDir, e.Name())

			// Read the superblock to get the true name and build ID
			img := &mkimagepb.ImageYAML{}
			yo := &sst.YamlObject{
				Path:   filepath.Join(imgDir, "image.yaml"),
				Schema: "sst.yaml.mkimage.v1.ImageYAML",
				Msg:    img,
			}
			if diags := yo.Load(ctx, log); diags == nil || !diags.HasAny() {
				if img.Provenance != nil && img.Provenance.Name != "" {
					_ = UpdateCatalog(ctx, log, "images", img.Provenance.Name, imgDir, img.Provenance.BuildId)
				}
			}
		}
	}

	// 2. Sync Instances
	instancesDir := filepath.Join(workspaceDir, "instances")
	if entries, err := os.ReadDir(instancesDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			instDir := filepath.Join(instancesDir, e.Name())

			// Read the superblock to get the true name
			inst := &runctlpb.Instance{}
			yo := &sst.YamlObject{
				Path:   filepath.Join(instDir, "instance.yaml"),
				Schema: "sst.yaml.runctl.v1.Instance",
				Msg:    inst,
			}
			if diags := yo.Load(ctx, log); diags == nil || !diags.HasAny() {
				if inst.Name != "" {
					// Use a hash of the directory path as a stable disambiguator for instances
					disambiguator := fmt.Sprintf("%x", sha256.Sum256([]byte(instDir)))[:8]
					_ = UpdateCatalog(ctx, log, "instances", inst.Name, instDir, disambiguator)
				}
			}
		}
	}
}

// ListCatalogNames returns a clean slice of all symlink names in a specific catalog directory (e.g., "images" or "instances").
func ListCatalogNames(kind string) ([]string, error) {
	catalogDir := filepath.Join(RuntimeDir(), kind)
	entries, err := os.ReadDir(catalogDir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
