// Package runtime implements `docksmith run`.
//
// FIX #1 — Uses isolation.Run (same primitive as the build engine RUN step).
//           The original used exec.Command directly; now the container process
//           runs inside Linux namespaces with a chroot to the assembled rootfs.
//
// FIX #6 — Supports -e KEY=VALUE environment overrides.  Image ENV values
//           are applied first; -e flags override them.
//
// FIX #7 — Network isolation is implicit: isolation.Run sets CLONE_NEWNET,
//           so the container has no external network interface.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"docksmith/build"
	"docksmith/isolation"
	"docksmith/storage"
)

// RunOptions controls `docksmith run`.
type RunOptions struct {
	// ImageRef is "name:tag".
	ImageRef string
	// CmdOverride replaces the image CMD when non-empty.
	CmdOverride []string
	// EnvOverrides are KEY=VALUE pairs from -e flags (FIX #6).
	EnvOverrides []string
}

// RunContainer assembles the image filesystem and starts an isolated container.
func RunContainer(opts RunOptions) error {
	// 1. Load manifest (FIX #8 — purely from local store).
	manifest, err := build.LoadImageManifest(opts.ImageRef)
	if err != nil {
		return fmt.Errorf("image %q not found: %w", opts.ImageRef, err)
	}

	// 2. Assemble rootfs in a temporary directory.
	rootfs, err := os.MkdirTemp("", "docksmith-container-*")
	if err != nil {
		return fmt.Errorf("failed to create rootfs: %w", err)
	}
	defer os.RemoveAll(rootfs)

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
			return fmt.Errorf("layer %s not found — image may be corrupt", hash)
		}
		if err := storage.ExtractTar(layerPath, rootfs); err != nil {
			return fmt.Errorf("failed to extract layer %s: %w", hash, err)
		}
	}

	// 3. Resolve CMD.
	argv := manifest.Config.Cmd
	if len(opts.CmdOverride) > 0 {
		argv = opts.CmdOverride
	}
	if len(argv) == 0 {
		return fmt.Errorf("no CMD defined in image and no command given at runtime")
	}

	// 4. Build environment: image ENV first, then -e overrides (FIX #6).
	envMap := map[string]string{}
	for _, kv := range manifest.Config.Env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	// -e KEY=VALUE overrides take precedence.
	for _, kv := range opts.EnvOverrides {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("-e %q is not in KEY=VALUE form", kv)
		}
		envMap[parts[0]] = parts[1]
	}
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}

	// 5. Working directory inside the container.
	workDir := manifest.Config.WorkingDir
	if workDir == "" {
		workDir = "/"
	}

	// Ensure workDir exists inside rootfs (base images always have /; custom
	// WORKDIR dirs should already be in the layers).
	os.MkdirAll(filepath.Join(rootfs, workDir), 0755) //nolint:errcheck

	// 6. Execute inside the isolated container (FIX #1).
	//    isolation.Run uses the SAME namespace+chroot mechanism as the build engine.
	fmt.Printf("Starting container from image %s (rootfs: %s)\n", opts.ImageRef, rootfs)

	if err := isolation.Run(isolation.RunOptions{
		Rootfs:  rootfs,
		WorkDir: workDir,
		Argv:    argv,
		Env:     env,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}); err != nil {
		return err
	}

	fmt.Printf("Container exited cleanly.\n")
	return nil
}