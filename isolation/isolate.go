// Package isolation provides the single primitive used for BOTH
// `docksmith run` and RUN during build: a new Linux mount+UTS+IPC+PID+NET
// namespace with a chroot into the container rootfs.
//
// FIX #1 — process isolation:
//   The original code called exec.Command directly on the host, giving
//   the command full access to the host filesystem and network.
//   We now use syscall.SysProcAttr with Cloneflags to create new namespaces
//   and Chroot to confine the process to the assembled rootfs.
//
// FIX #7 — offline enforcement (network namespace):
//   CLONE_NEWNET creates a fresh network namespace with no external
//   interfaces, so the process cannot reach the internet.
//
// CRITICAL path-resolution note:
//   When SysProcAttr.Chroot is set the kernel executes:
//     chroot(Chroot) → chdir(cmd.Dir) → execve(cmd.Path, ...)
//   Therefore cmd.Path must be the path INSIDE the new root (e.g. "/bin/sh"),
//   NOT the host-side absolute path (e.g. "/tmp/docksmith-build-xxx/bin/sh").
//   exec.Command() calls LookPath which produces the host path → ENOENT after
//   chroot.  We bypass this by constructing exec.Cmd directly and setting
//   cmd.Path to the in-chroot path, after verifying the binary exists on the
//   host side.
package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// RunOptions configures a single isolated execution.
type RunOptions struct {
	// Rootfs is the assembled container filesystem root on the host.
	Rootfs string
	// WorkDir is the working directory INSIDE the container (e.g. "/app").
	// Defaults to "/" if empty.
	WorkDir string
	// Argv is [binary, arg0, arg1, ...].
	// binary must be an absolute path inside the container (e.g. "/bin/sh",
	// "/app/myapp").  Bare names are searched in standard PATH dirs.
	Argv []string
	// Env is the full environment slice ("KEY=value") for the process.
	Env []string
	// Stdin/Stdout/Stderr — defaults to os.Stdin/Stdout/Stderr when nil.
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File
}

// Run executes a command inside an isolated container environment.
// This is the SINGLE isolation primitive used by both the build engine
// (RUN instruction) and the runtime (docksmith run).
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

	// Resolve the binary to an absolute in-chroot path and verify it exists
	// on the host before we hand off to the kernel.
	inChrootPath, err := resolveInChroot(opts.Rootfs, opts.Argv[0])
	if err != nil {
		return fmt.Errorf("isolation.Run: %w", err)
	}

	// Build exec.Cmd manually — do NOT use exec.Command() because it calls
	// LookPath and overwrites cmd.Path with the host absolute path, which is
	// invalid after the kernel applies the chroot.
	//
	// cmd.Path  = in-chroot path  → kernel resolves this after chroot
	// cmd.Args  = full argv slice (Args[0] is conventionally the program name)
	// cmd.Dir   = working dir inside the chroot
	cmd := &exec.Cmd{
		Path:   inChrootPath,
		Args:   opts.Argv,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Env:    opts.Env,
		Dir:    workDir,
	}

	// FIX #1 — Linux namespaces + chroot.
	// CLONE_NEWNS  : new mount namespace  (container mounts don't affect host)
	// CLONE_NEWUTS : new UTS namespace    (container hostname isolated)
	// CLONE_NEWIPC : new IPC namespace    (SysV IPC isolated)
	// CLONE_NEWPID : new PID namespace    (container PID 1 is the process)
	// CLONE_NEWNET : new network namespace (FIX #7 — no outbound network)
	//
	// Chroot: rootfs — kernel chroots before exec, so the process can only
	// see what is under rootfs.  A file written inside CANNOT appear on host.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWIPC |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNET,
		Chroot: opts.Rootfs,
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container process exited with error: %w", err)
	}
	return nil
}

// RunShell runs a shell command string inside the isolated rootfs.
// Used by the build engine for every RUN instruction.
func RunShell(rootfs, workDir, shellCmd string, env []string) error {
	return Run(RunOptions{
		Rootfs:  rootfs,
		WorkDir: workDir,
		Argv:    []string{"/bin/sh", "-c", shellCmd},
		Env:     env,
	})
}

// resolveInChroot returns the absolute path of binary INSIDE the container
// (e.g. "/bin/sh") after confirming it exists on the host at rootfs+path.
//
// If binary is already absolute (starts with "/") we just validate it.
// If it is bare (e.g. "myapp") we search the standard PATH directories.
func resolveInChroot(rootfs, binary string) (string, error) {
	// Absolute in-container path — validate on host and return as-is.
	if strings.HasPrefix(binary, "/") {
		hostPath := filepath.Join(rootfs, binary)
		info, err := os.Stat(hostPath)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("binary %q not found in rootfs", binary)
		}
		return binary, nil
	}

	// Bare name — search standard PATH locations inside rootfs.
	searchDirs := []string{
		"/bin", "/usr/bin", "/usr/local/bin", "/sbin", "/usr/sbin",
	}
	for _, dir := range searchDirs {
		inChroot := filepath.Join(dir, binary)
		hostPath := filepath.Join(rootfs, inChroot)
		info, err := os.Stat(hostPath)
		if err == nil && !info.IsDir() {
			return inChroot, nil
		}
	}

	return "", fmt.Errorf("binary %q not found in rootfs %s (searched PATH)", binary, rootfs)
}