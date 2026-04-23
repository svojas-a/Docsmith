package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SaveLayer stores a tar file using its SHA256 hash as the filename.
func SaveLayer(tarPath string) (string, error) {
	hash, err := ComputeSHA256(tarPath)
	if err != nil {
		return "", fmt.Errorf("SaveLayer hash: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("SaveLayer homedir: %w", err)
	}
	layersDir := filepath.Join(homeDir, ".docksmith", "layers")
	if err := os.MkdirAll(layersDir, 0755); err != nil {
		return "", fmt.Errorf("SaveLayer mkdir: %w", err)
	}
	layerPath := filepath.Join(layersDir, hash+".tar")

	// Already exists — dedup.
	if _, err := os.Stat(layerPath); err == nil {
		os.Remove(tarPath)
		return hash, nil
	}

	// Cross-filesystem safe copy (avoids Rename failures across tmpfs→ext4/NTFS).
	if err := copyFileRaw(tarPath, layerPath); err != nil {
		return "", fmt.Errorf("SaveLayer copy: %w", err)
	}
	os.Remove(tarPath)
	return hash, nil
}

// LoadLayer returns the full path of a stored layer by its hash.
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