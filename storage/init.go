package storage

import (
	"os"
	"path/filepath"
)

// InitStorage creates the required ~/.docksmith directory structure.
func InitStorage() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	basePath := filepath.Join(homeDir, ".docksmith")
	dirs := []string{
		filepath.Join(basePath, "images"),
		filepath.Join(basePath, "layers"),
		filepath.Join(basePath, "cache"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}