package storage

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func CreateTar(sourceDir string, tarPath string) error {
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	tarWriter := tar.NewWriter(tarFile)
	defer tarWriter.Close()

	return filepath.Walk(sourceDir, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip unwanted files
		if strings.Contains(file, ".git") || strings.Contains(file, "layer.tar") || strings.Contains(file, ".DS_Store") {
			return nil
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		// 🔥 Preserve permissions (VERY IMPORTANT)
		header.Mode = int64(info.Mode())

		// Get relative path
		relPath, err := filepath.Rel(sourceDir, file)
		if err != nil {
			return err
		}
		header.Name = relPath

		// Write header (for BOTH file + directory)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// If directory → no content to write
		if info.IsDir() {
			return nil
		}

		// Open file
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()

		// Copy file content into tar
		_, err = io.Copy(tarWriter, f)
		return err
	})
}

// ExtractTar extracts a tar archive into a directory, handling symlinks and special files
func ExtractTar(tarPath string, destDir string) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	tarReader := tar.NewReader(file)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Clean the path and skip anything trying to escape destDir
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			continue
		}

		targetPath := filepath.Join(destDir, cleanName)

		switch header.Typeflag {

		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)|0755); err != nil {
				return err
			}

		case tar.TypeReg, tar.TypeRegA:
			// Ensure parent dir exists
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			// Remove existing file/symlink at this path before writing
			os.Remove(targetPath)
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(outFile, tarReader)
			outFile.Close()
			if err != nil {
				return err
			}

		case tar.TypeSymlink:
			// Remove existing entry before creating symlink
			os.Remove(targetPath)
			os.MkdirAll(filepath.Dir(targetPath), 0755)
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				// Ignore symlink errors (e.g. already exists)
				continue
			}

		case tar.TypeLink:
			// Hard link
			linkTarget := filepath.Join(destDir, filepath.Clean(header.Linkname))
			os.Remove(targetPath)
			os.MkdirAll(filepath.Dir(targetPath), 0755)
			if err := os.Link(linkTarget, targetPath); err != nil {
				continue
			}

		default:
			// Skip special files (dev/console, sockets, etc.) - safe to ignore
			continue
		}
	}

	return nil
}
