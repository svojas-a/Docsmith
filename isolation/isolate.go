package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// childEnvKey is the env var used to pass container parameters to the
// re-exec'd child process.
const childEnvKey = "__DOCKSMITH_CONTAINER"

// MaybeRunAsContainerChild must be called at the very start of main().
// If this process was re-executed as a container child, it sets up the
// container (pivot_root, exec) and never returns. Otherwise it returns.
func MaybeRunAsContainerChild() {
	val := os.Getenv(childEnvKey)
	if val == "" {
		return
	}
	// val = "rootfs\x00workdir\x00binary\x00arg0\x00arg1..."
	parts := strings.Split(val, "\x01")
	if len(parts) < 3 {
		fmt.Fprintln(os.Stderr, "docksmith: malformed container env")
		os.Exit(1)
	}
	rootfs := parts[0]
	workDir := parts[1]
	binary := parts[2]
	argv := parts[2:] // binary is argv[0]

	if workDir == "" {
		workDir = "/"
	}

	// ── Step 1: bind-mount rootfs onto itself ─────────────────────────────
	// Required for pivot_root: new root must be a mount point.
	// Works in a user-owned mount namespace (CLONE_NEWUSER | CLONE_NEWNS).
	if err := syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		fmt.Fprintf(os.Stderr, "docksmith: bind mount: %v\n", err)
		os.Exit(1)
	}

	// ── Step 2: create a directory inside rootfs for the old root ─────────
	oldRoot := filepath.Join(rootfs, ".old_root")
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "docksmith: mkdir old_root: %v\n", err)
		os.Exit(1)
	}

	// ── Step 3: pivot_root ────────────────────────────────────────────────
	// Makes rootfs the new filesystem root.
	// Old root is moved to /.old_root inside the new root.
	if err := syscall.PivotRoot(rootfs, oldRoot); err != nil {
		fmt.Fprintf(os.Stderr, "docksmith: pivot_root: %v\n", err)
		os.Exit(1)
	}
	syscall.Chdir("/")

	// ── Step 4: unmount old root ──────────────────────────────────────────
	// After pivot_root, the container process can no longer see the host fs.
	syscall.Unmount("/.old_root", syscall.MNT_DETACH) //nolint:errcheck
	os.Remove("/.old_root")

	// ── Step 5: chdir to working directory ───────────────────────────────
	if workDir != "/" {
		if err := syscall.Chdir(workDir); err != nil {
			// Non-fatal: workdir may not exist in this image.
			syscall.Chdir("/") //nolint:errcheck
		}
	}

	// ── Step 6: exec the real binary ─────────────────────────────────────
	os.Unsetenv(childEnvKey)
	if err := syscall.Exec(binary, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "docksmith: exec %s: %v\n", binary, err)
		os.Exit(1)
	}
}

// ── RunOptions ────────────────────────────────────────────────────────────────

type RunOptions struct {
	Rootfs    string
	WorkDir   string
	Argv      []string
	Env       []string
	BuildMode bool
	Stdin     *os.File
	Stdout    *os.File
	Stderr    *os.File
}

// Run is the single entry point for build and runtime execution.
func Run(opts RunOptions) error {
	if len(opts.Argv) == 0 {
		return fmt.Errorf("isolation.Run: no command specified")
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "/"
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if opts.BuildMode {
		return runBuildMode(opts, workDir, stdin, stdout, stderr)
	}
	return runRuntimeMode(opts, workDir, stdin, stdout, stderr)
}

// ── Build mode ────────────────────────────────────────────────────────────────

func runBuildMode(opts RunOptions, workDir string, stdin, stdout, stderr *os.File) error {
	hostSh, err := exec.LookPath("sh")
	if err != nil {
		hostSh = "/bin/sh"
	}
	hostWorkDir := filepath.Join(opts.Rootfs, workDir)
	if err := os.MkdirAll(hostWorkDir, 0755); err != nil {
		return fmt.Errorf("isolation (build): %w", err)
	}
	cmd := exec.Command(hostSh, "-c", strings.Join(opts.Argv, " "))
	cmd.Dir = hostWorkDir
	cmd.Env = opts.Env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("RUN command failed: %w", err)
	}
	return nil
}

// ── Runtime mode ──────────────────────────────────────────────────────────────
//
// Re-exec pattern with pivot_root:
//
//  Parent: fork /proc/self/exe (this binary) into CLONE_NEWUSER + CLONE_NEWNS.
//          Go writes uid/gid maps from the parent (allowed).
//          Child env var carries rootfs, workdir, binary, argv.
//
//  Child:  MaybeRunAsContainerChild() runs at start of main().
//          bind-mount rootfs → pivot_root → unmount old root → exec binary.
//          After pivot_root, the child has no access to the host filesystem.
//
// This works without root on standard Linux (Ubuntu 18.04+) because:
//  • CLONE_NEWUSER: user namespace where process has full capabilities.
//  • CLONE_NEWNS:   mount namespace owned by the user namespace.
//  • bind-mount + pivot_root: allowed in a user-owned mount namespace.

func runRuntimeMode(opts RunOptions, workDir string, stdin, stdout, stderr *os.File) error {
	inChrootPath, err := resolveInChroot(opts.Rootfs, opts.WorkDir, opts.Argv[0])
	if err != nil {
		return fmt.Errorf("isolation (runtime): %w", err)
	}

	// Encode all parameters as a NUL-separated string in an env var.
	params := append([]string{opts.Rootfs, workDir}, inChrootPath)
	if len(opts.Argv) > 1 {
		params = append(params, opts.Argv[1:]...)
	}
	// params[2:] = inChrootPath + additional args = full argv
	childVal := strings.Join(params, "\x01")

	childEnv := make([]string, 0, len(opts.Env)+1)
	for _, e := range opts.Env {
		if !strings.HasPrefix(e, childEnvKey+"=") {
			childEnv = append(childEnv, e)
		}
	}
	childEnv = append(childEnv, childEnvKey+"="+childVal)

	uid := syscall.Getuid()
	gid := syscall.Getgid()

	exe := "/proc/self/exe"

	cmd := &exec.Cmd{
		Path:   exe,
		Args:   []string{"docksmith-container"},
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Env:    childEnv,
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
			UidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: uid, Size: 1},
			},
			GidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: gid, Size: 1},
			},
		},
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container process exited with error: %w", err)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func RunBuild(rootfs, workDir, command string, env []string) error {
	return Run(RunOptions{Rootfs: rootfs, WorkDir: workDir, Argv: []string{command}, Env: env, BuildMode: true})
}

func RunContainer(rootfs, workDir string, argv []string, env []string) error {
	return Run(RunOptions{Rootfs: rootfs, WorkDir: workDir, Argv: argv, Env: env, BuildMode: false})
}

func RunShell(rootfs, workDir, shellCmd string, env []string) error {
	return Run(RunOptions{Rootfs: rootfs, WorkDir: workDir, Argv: []string{"/bin/sh", "-c", shellCmd}, Env: env, BuildMode: false})
}

func resolveInChroot(rootfs, workDir, binary string) (string, error) {
    if strings.HasPrefix(binary, "/") {
        hostPath := filepath.Join(rootfs, binary)
        info, err := os.Stat(hostPath)
        if err != nil || info.IsDir() {
            return "", fmt.Errorf("binary %q not found in container image", binary)
        }
        return binary, nil
    }

    // Check WORKDIR first — critical for scratch images
    searchDirs := []string{}
    if workDir != "" && workDir != "/" {
        searchDirs = append(searchDirs, workDir)
    }
    searchDirs = append(searchDirs, "/bin", "/usr/bin", "/usr/local/bin", "/sbin", "/usr/sbin")

    for _, dir := range searchDirs {
        inChroot := filepath.Join(dir, binary)
        hostPath := filepath.Join(rootfs, inChroot)
        info, err := os.Stat(hostPath)
        if err == nil && !info.IsDir() {
            return inChroot, nil
        }
    }
    return "", fmt.Errorf("binary %q not found in container image (searched PATH)", binary)
}
