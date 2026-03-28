package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// SaveLayer stores a tar file using its SHA256 hash
func SaveLayer(tarPath string) (string, error) {

	// Step 1: Compute hash of tar file
	hash, err := ComputeSHA256(tarPath)
	if err != nil {
		return "", err
	}

	// Step 2: Get ~/.docksmith/layers path
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	layerPath := filepath.Join(homeDir, ".docksmith", "layers", hash+".tar")

	// Step 3: Check if already exists (avoid duplicates)
	if _, err := os.Stat(layerPath); err == nil {
		fmt.Println("Layer already exists (cache reuse):", hash)
		return hash, nil
	}

	// Step 4: Move tar file to layers folder
	err = os.Rename(tarPath, layerPath)
	if err != nil {
		return "", err
	}

	fmt.Println("Layer saved:", hash)

	return hash, nil
}

// LoadLayer returns full path of a stored layer using its hash
func LoadLayer(hash string) (string, error) {

	// Step 1: Get home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Step 2: Construct layer path
	layerPath := filepath.Join(homeDir, ".docksmith", "layers", hash+".tar")

	// Step 3: Check if exists
	if _, err := os.Stat(layerPath); os.IsNotExist(err) {
		return "", fmt.Errorf("layer not found: %s", hash)
	}

	return layerPath, nil
}