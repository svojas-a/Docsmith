package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SaveLayer stores a tar file using its SHA256 hash
func SaveLayer(tarPath string) (string, error) {

	// Step 1: Compute hash of tar file
	hash, err := ComputeSHA256(tarPath)
	if err != nil {
		return "", fmt.Errorf("SaveLayer hash: %w", err)
	}

	// Step 2: Get ~/.docksmith/layers path
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("SaveLayer homedir: %w", err)
	}

	layersDir := filepath.Join(homeDir, ".docksmith", "layers")
	if err := os.MkdirAll(layersDir, 0755); err != nil {
		return "", fmt.Errorf("SaveLayer mkdir: %w", err)
	}

	layerPath := filepath.Join(layersDir, hash+".tar")

	// Step 3: Check if already exists (avoid duplicates)
	if _, err := os.Stat(layerPath); err == nil {
		fmt.Println("Layer already exists (cache reuse):", hash)
		os.Remove(tarPath) // clean up temp file
		return hash, nil
	}

	// Step 4: Copy tar file to layers folder
	// FIX: Use copy+delete instead of os.Rename because Rename fails when
	// src (/tmp = tmpfs) and dst (~/.docksmith = ext4/NTFS) are on different
	// filesystems — this is common in WSL environments.
	if err := copyFileRaw(tarPath, layerPath); err != nil {
		return "", fmt.Errorf("SaveLayer copy: %w", err)
	}
	os.Remove(tarPath) // clean up temp file after successful copy

	fmt.Println("Layer saved:", hash)
	return hash, nil
}

// LoadLayer returns full path of a stored layer using its hash
func LoadLayer(hash string) (string, error) {

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("LoadLayer homedir: %w", err)
	}

	layerPath := filepath.Join(homeDir, ".docksmith", "layers", hash+".tar")

	if _, err := os.Stat(layerPath); os.IsNotExist(err) {
		return "", fmt.Errorf("layer not found: %s", hash)
	}

	return layerPath, nil
}

// copyFileRaw copies src to dst byte-for-byte.
// Used instead of os.Rename to safely move files across filesystem boundaries.
func copyFileRaw(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
