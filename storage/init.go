package storage

import (
	"os"
	"path/filepath"
)

// InitStorage creates the required ~/.docksmith directory structure
func InitStorage() error {

	// Step 1: Get user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Step 2: Define base path (~/.docksmith)
	basePath := filepath.Join(homeDir, ".docksmith")

	// Step 3: Define subdirectories
	dirs := []string{
		filepath.Join(basePath, "images"),
		filepath.Join(basePath, "layers"),
		filepath.Join(basePath, "cache"),
	}

	// Step 4: Create directories if not exist
	for _, dir := range dirs {
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return err
		}
	}

	return nil
}