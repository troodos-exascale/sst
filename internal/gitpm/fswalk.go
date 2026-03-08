package gitpm

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

type NodeType int

const (
	NodeDir NodeType = iota
	NodeFile
	NodeSymlink
	NodeHardlink
)

type Node struct {
	// Path on disk
	Abs string
	// Path inside package (prefix + relative)
	Rel string
	// Type
	Type NodeType
	// Symlink target (for NodeSymlink)
	Linkname string
	// Hardlink target path inside package (for NodeHardlink)
	HardlinkTarget string

	// Normalized metadata
	Mode  fs.FileMode
	UID   int
	GID   int
	Size  int64
	MTime time.Time
	UName string // left blank (root) for determinism
	GName string // left blank (root) for determinism
}

// CollectTree walks base and returns a deterministic list of Nodes.
// prefix is prepended to the relative path inside the package (e.g. "/usr/local").
// includeRoot controls whether the root directory itself is emitted as a NodeDir.
func FSWalk(base, prefix string, includeRoot bool) ([]Node, error) {
	b, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}

	// inode map for hardlinks, etc.
	type inodeKey struct {
		dev uint64
		ino uint64
	}
	firstPathByInode := map[inodeKey]string{}
	var nodes []Node
	var paths []string

	if err := filepath.Walk(b, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })

	fixed := time.Unix(0, 0)

	for _, p := range paths {
		rel, _ := filepath.Rel(b, p)

		if rel == "." {
			if includeRoot {
				info, _ := os.Lstat(p)
				n := Node{
					Abs:   p,
					Rel:   filepath.ToSlash(strings.TrimPrefix(prefix, "/")),
					Type:  NodeDir,
					Mode:  info.Mode().Perm() | os.ModeDir,
					UID:   0,
					GID:   0,
					Size:  0,
					MTime: fixed,
				}
				nodes = append(nodes, n)
			}
			continue
		}

		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}

		cleanPrefix := strings.TrimPrefix(prefix, "/")
		node := Node{
			Abs:   p,
			Rel:   filepath.ToSlash(filepath.Join(cleanPrefix, rel)),
			Mode:  info.Mode(),
			UID:   0,
			GID:   0,
			MTime: fixed,
		}

		// Best-effort real uid/gid if available (won't break determinism as we force to 0 later in writers if desired)
		if sys, ok := info.Sys().(*syscall.Stat_t); ok && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
			node.UID = int(sys.Uid)
			node.GID = int(sys.Gid)
			// For determinism we’ll write UID/GID as 0 in writers, but keep the info here if we later want policy.
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			node.Type = NodeSymlink
			node.Linkname = target
			node.Mode = 0777 | os.ModeSymlink // symlink perms ignored by tar, but normalize

		case info.IsDir():
			node.Type = NodeDir
			// keep directory perm bits only
			node.Mode = (info.Mode() & os.ModePerm) | os.ModeDir
			node.Size = 0

		case info.Mode().IsRegular():
			// detect hardlinks (same inode+dev seen before)
			if sys, ok := info.Sys().(*syscall.Stat_t); ok {
				ik := inodeKey{dev: uint64(sys.Dev), ino: uint64(sys.Ino)}
				if prev, ok := firstPathByInode[ik]; ok && sys.Nlink > 1 {
					node.Type = NodeHardlink
					node.HardlinkTarget = filepath.ToSlash(filepath.Join(prefix, mustRel(b, prev)))
					// normalize file mode without dir bits
					node.Mode = info.Mode().Perm()
					break
				}
				firstPathByInode[ik] = p
			}
			node.Type = NodeFile
			node.Size = info.Size()
			node.Mode = info.Mode().Perm()

		default:
			// ignore devices/fifos for now (can add later)
			continue
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func mustRel(base, p string) string {
	rel, _ := filepath.Rel(base, p)
	return rel
}
