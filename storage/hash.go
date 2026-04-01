package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// ComputeSHA256 calculates SHA256 hash of a file
func ComputeSHA256(filePath string) (string, error) {

	// Step 1: Open file
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Step 2: Create new SHA256 hash object
	hasher := sha256.New()

	// Step 3: Copy file content into hasher
	_, err = io.Copy(hasher, file)
	if err != nil {
		return "", err
	}

	// Step 4: Convert hash to string
	hashBytes := hasher.Sum(nil)

	// Step 5: Convert bytes → hex string
	hashString := hex.EncodeToString(hashBytes)

	return hashString, nil
}