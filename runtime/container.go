package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"docksmith/build"
	"docksmith/storage"
)

func RunContainer(imageRef string) error {

	// 1. Load manifest
	manifest, err := build.LoadImageManifest(imageRef)
	if err != nil {
		return fmt.Errorf("failed to load image: %w", err)
	}

	// 2. Create container rootfs
	rootfs, err := os.MkdirTemp("", "docksmith-container-*")
	if err != nil {
		return fmt.Errorf("failed to create container rootfs: %w", err)
	}
	//defer os.RemoveAll(rootfs)
	// DEBUG: Print rootfs contents after extraction
	fmt.Println("=== ROOTFS CONTENTS ===")
	filepath.Walk(rootfs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(rootfs, path)
		fmt.Printf("  %s (mode=%s, size=%d)\n", rel, info.Mode(), info.Size())
		return nil
	})
	fmt.Println("=======================")

	fmt.Println("Container rootfs:", rootfs)

	// 3. Extract layers in order
	for _, layer := range manifest.Layers {
		if layer.Digest == "" {
			continue
		}

		hash := strings.TrimPrefix(layer.Digest, "sha256:")
		if hash == "" {
			continue
		}

		layerPath, err := storage.LoadLayer(hash)
		if err != nil {
			return fmt.Errorf("layer not found: %s", hash)
		}

		if err = storage.ExtractTar(layerPath, rootfs); err != nil {
			return fmt.Errorf("failed to extract layer %s: %w", hash, err)
		}
	}

	// 4. Resolve working directory
	containerDir := rootfs
	if manifest.Config.WorkingDir != "" {
		containerDir = filepath.Join(rootfs, manifest.Config.WorkingDir)
		if err := os.MkdirAll(containerDir, 0755); err != nil {
			return fmt.Errorf("failed to create workdir: %w", err)
		}
	}

	// 5. Resolve CMD
	cmdArgs := manifest.Config.Cmd
	if len(cmdArgs) == 0 {
		return fmt.Errorf("no CMD specified in image")
	}

	// 6. Resolve binary path relative to containerDir
	binaryPath := cmdArgs[0]
	if !filepath.IsAbs(binaryPath) {
		binaryPath = filepath.Join(containerDir, strings.TrimPrefix(binaryPath, "./"))
	}

	// 7. Verify binary exists
	if _, err := os.Stat(binaryPath); err != nil {
		return fmt.Errorf("binary not found at %s: %w", binaryPath, err)
	}

	// 8. Ensure executable bit is set
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod binary: %w", err)
	}

	// 9. Prepare environment
	env := os.Environ()
	env = append(env, manifest.Config.Env...)

	fmt.Println("Executing:", binaryPath)
	fmt.Println("Working Dir:", containerDir)

	// 10. Execute directly — no shell, no namespace flags (WSL2 compatible)
	cmd := exec.Command(binaryPath, cmdArgs[1:]...)
	cmd.Dir = containerDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	fmt.Println("Running container...")
	return cmd.Run()
}