package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// ComputeSHA256 calculates SHA256 hash of a file.
func ComputeSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}