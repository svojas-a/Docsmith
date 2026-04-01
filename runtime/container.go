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
	// defer os.RemoveAll(rootfs)

	// 3. Extract layers in order FIRST, then debug print
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

	// DEBUG: Print rootfs contents AFTER extraction so we see what's actually there
	
	fmt.Println("Container rootfs:", rootfs)

	// 4. Resolve working directory
	workDir := manifest.Config.WorkingDir
	containerDir := rootfs
	if workDir != "" {
		// Strip leading slash so filepath.Join works correctly
		containerDir = filepath.Join(rootfs, filepath.Clean(workDir))
		if err := os.MkdirAll(containerDir, 0755); err != nil {
			return fmt.Errorf("failed to create workdir: %w", err)
		}
	}

	// 5. Resolve CMD
	cmdArgs := manifest.Config.Cmd
	if len(cmdArgs) == 0 {
		return fmt.Errorf("no CMD specified in image")
	}

	// 6. Resolve binary path
	// CMD ["app"] + WORKDIR /app → look for binary in rootfs first,
	// then fall back to containerDir (workdir).
	// go build -o app writes to cmd.Dir which is buildRoot/workDir,
	// so the binary lives at rootfs/workDir/app after layer extraction.
	binaryName := strings.TrimPrefix(cmdArgs[0], "./")
	binaryPath := ""

	// Try rootfs/<workdir>/<binary> first (most common case)
	candidate1 := filepath.Join(containerDir, binaryName)
	// Try rootfs/<binary> as fallback (binary placed at root)
	candidate2 := filepath.Join(rootfs, binaryName)

	if _, err := os.Stat(candidate1); err == nil {
		binaryPath = candidate1
	} else if _, err := os.Stat(candidate2); err == nil {
		binaryPath = candidate2
	} else {
		return fmt.Errorf("binary %q not found — tried:\n  %s\n  %s", binaryName, candidate1, candidate2)
	}

	// 7. Ensure executable bit is set
	if err := os.Chmod(binaryPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod binary: %w", err)
	}

	// 8. Prepare environment
	env := os.Environ()
	env = append(env, manifest.Config.Env...)

	fmt.Println("Executing:", binaryPath)
	fmt.Println("Working Dir:", containerDir)

	// 9. Execute directly — no shell, no namespace flags (WSL2 compatible)
	cmd := exec.Command(binaryPath, cmdArgs[1:]...)
	cmd.Dir = containerDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	fmt.Println("Running container...")
	return cmd.Run()
}
