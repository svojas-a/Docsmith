package storage

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CreateTar archives sourceDir into tarPath.
// FULL SNAPSHOT — used only when no baseline exists (FROM scratch first layer).
// Use CreateDeltaTar for layer-producing steps.
func CreateTar(sourceDir string, tarPath string) error {
	return createTarFiltered(sourceDir, tarPath, nil)
}

// CreateDeltaTar creates a tar of ONLY files that were added or modified
// compared to baselineDir.  This implements delta (not snapshot) layers.
//
// FIX #2 — delta layers:
//   The original CreateTar archived the entire sourceDir every time,
//   so every layer contained the full filesystem.  The spec requires each
//   COPY/RUN layer to contain only the files changed by that step.
//   We walk sourceDir, skip files whose path+mtime+size match the baseline,
//   and include everything else.
//
// FIX #3 — deterministic tar:
//   Entries are added in lexicographically sorted order and all timestamps
//   are zeroed so that identical content always produces an identical digest.
func CreateDeltaTar(sourceDir, baselineDir, tarPath string) error {
	return createTarFiltered(sourceDir, tarPath, func(rel string, info os.FileInfo) bool {
		// Always include directories so the receiver can reconstruct the tree.
		if info.IsDir() {
			return true
		}
		baselinePath := filepath.Join(baselineDir, rel)
		bi, err := os.Lstat(baselinePath)
		if err != nil {
			// File doesn't exist in baseline → include it.
			return true
		}
		// Include if size or mtime differ.
		return info.Size() != bi.Size() || info.ModTime() != bi.ModTime()
	})
}

// createTarFiltered is the shared implementation.
// include(rel, info) returns true when the entry should be archived.
// nil means include everything (full snapshot).
func createTarFiltered(sourceDir, tarPath string, include func(string, os.FileInfo) bool) error {
	// Collect all entries first so we can sort them (FIX #3).
	type entry struct {
		abs string
		rel string
		info os.FileInfo
	}
	var entries []entry

	err := filepath.Walk(sourceDir, func(abs string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, abs)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Skip files that pollute the layer.
		base := filepath.Base(abs)
		if strings.Contains(rel, ".git") ||
			strings.Contains(rel, "layer.tar") ||
			strings.Contains(rel, ".DS_Store") ||
			base == "gopath" ||
			base == "gocache" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if include != nil && !include(rel, info) {
			return nil
		}

		entries = append(entries, entry{abs, rel, info})
		return nil
	})
	if err != nil {
		return err
	}

	// FIX #3 — sort lexicographically for reproducibility.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].rel < entries[j].rel
	})

	tarFile, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	tw := tar.NewWriter(tarFile)
	defer tw.Close()

	// Epoch zero used for all timestamps (FIX #3).
	epoch := time.Time{}

	for _, e := range entries {
		hdr, err := tar.FileInfoHeader(e.info, "")
		if err != nil {
			return err
		}
		hdr.Name = e.rel
		hdr.Mode = int64(e.info.Mode())

		// FIX #3 — zero all timestamps so digest is content-only.
		hdr.ModTime = epoch
		hdr.AccessTime = epoch
		hdr.ChangeTime = epoch

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if e.info.IsDir() {
			continue
		}

		f, err := os.Open(e.abs)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// ExtractTar extracts a tar archive into destDir.
// Handles regular files, directories, symlinks and hard links.
func ExtractTar(tarPath string, destDir string) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	tr := tar.NewReader(file)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Path traversal guard.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") {
			continue
		}
		target := filepath.Join(destDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)|0755); err != nil {
				return err
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}

		case tar.TypeSymlink:
			os.Remove(target)
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Symlink(hdr.Linkname, target) //nolint:errcheck

		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, filepath.Clean(hdr.Linkname))
			os.Remove(target)
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Link(linkTarget, target) //nolint:errcheck
		}
	}
	return nil
}