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

		// ❌ Skip directories
		if info.IsDir() {
			return nil
		}

		// ❌ Skip unwanted files
		if strings.Contains(file, ".git") || strings.Contains(file, "layer.tar") || strings.Contains(file, ".DS_Store") {
			return nil
		}

		// Open file
		f, err := os.Open(file)
		if err != nil {
			return err
		}

		// Create header
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			f.Close()
			return err
		}

		// Relative path
		relPath, err := filepath.Rel(sourceDir, file)
		if err != nil {
			f.Close()
			return err
		}
		header.Name = relPath

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			f.Close()
			return err
		}

		// Write content
		_, err = io.Copy(tarWriter, f)

		// CLOSE FILE HERE ✅
		f.Close()

		return err
	})
}


// ExtractTar extracts a tar archive into a directory
func ExtractTar(tarPath string, destDir string) error {

	// Step 1: Open tar file
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Step 2: Create tar reader
	tarReader := tar.NewReader(file)

	for {
		// Step 3: Read next file
		header, err := tarReader.Next()

		if err == io.EOF {
			break // End of tar
		}
		if err != nil {
			return err
		}

		// Step 4: Create full path
		targetPath := filepath.Join(destDir, header.Name)

		// Step 5: Create directories if needed
		err = os.MkdirAll(filepath.Dir(targetPath), 0755)
		if err != nil {
			return err
		}

		// Step 6: Create file
		outFile, err := os.Create(targetPath)
		if err != nil {
			return err
		}

		// Step 7: Copy content
		_, err = io.Copy(outFile, tarReader)
		outFile.Close()

		if err != nil {
			return err
		}
	}

	return nil
}