// Docksmith CLI — single binary, no daemon.
//
// FIX #5 — docksmith images: prints Name / Tag / ID (12-char digest) / Created
//           docksmith rmi:    removes manifest AND all associated layer files
// FIX #6 — docksmith run:    -e KEY=VALUE flag (repeatable) wired through to runtime
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"docksmith/build"
	"docksmith/isolation"
	"docksmith/parser"
	"docksmith/runtime"
	"docksmith/storage"
)

func main() {
	isolation.MaybeRunAsContainerChild()
	if err := storage.InitStorage(); err != nil {
		fmt.Fprintf(os.Stderr, "storage init failed: %v\n", err)
		os.Exit(1)
	}

	args := os.Args
	if len(args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch args[1] {
	case "build":
		err = cmdBuild(args[2:])
	case "run":
		err = cmdRun(args[2:])
	case "images":
		err = cmdImages()
	case "rmi":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: docksmith rmi <name:tag>")
			os.Exit(1)
		}
		err = cmdRmi(args[2])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// ── build ─────────────────────────────────────────────────────────────────────

func cmdBuild(args []string) error {
	tag := "latest"
	contextDir := "."
	noCache := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			i++
			if i >= len(args) {
				return fmt.Errorf("-t requires an argument")
			}
			tag = args[i]
		case "--no-cache":
			noCache = true
		default:
			contextDir = args[i]
		}
	}

	docksmithfile := filepath.Join(contextDir, "Docksmithfile")
	instructions, err := parser.ParseDocksmithfile(docksmithfile)
	if err != nil {
		return fmt.Errorf("parse error: %w", err)
	}

	return build.Run(instructions, build.BuildOptions{
		Tag:     tag,
		Context: contextDir,
		NoCache: noCache,
	})
}

// ── run ───────────────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: docksmith run [-e KEY=VALUE] <name:tag> [cmd ...]")
	}

	var envOverrides []string
	var positional []string

	for i := 0; i < len(args); i++ {
		if args[i] == "-e" {
			i++
			if i >= len(args) {
				return fmt.Errorf("-e requires KEY=VALUE argument")
			}
			envOverrides = append(envOverrides, args[i])
		} else {
			positional = append(positional, args[i])
		}
	}

	if len(positional) == 0 {
		return fmt.Errorf("usage: docksmith run [-e KEY=VALUE] <name:tag> [cmd ...]")
	}

	imageRef := positional[0]
	var cmdOverride []string
	if len(positional) > 1 {
		cmdOverride = positional[1:]
	}

	return runtime.RunContainer(runtime.RunOptions{
		ImageRef:     imageRef,
		CmdOverride:  cmdOverride,
		EnvOverrides: envOverrides,
	})
}

// ── images ───────────────────────────────────────────────────────────────────
// FIX #5 — columns: Name | Tag | ID (12 chars of digest) | Created

func cmdImages() error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".docksmith", "images")

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No images.")
			return nil
		}
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tTAG\tID\tCREATED")

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		var m struct {
			Name    string `json:"name"`
			Tag     string `json:"tag"`
			Digest  string `json:"digest"`
			Created string `json:"created"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		// Short ID: first 12 chars after "sha256:".
		shortID := strings.TrimPrefix(m.Digest, "sha256:")
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		// Human-readable created time.
		created := m.Created
		if t, err := time.Parse(time.RFC3339, m.Created); err == nil {
			created = humanTime(t)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Name, m.Tag, shortID, created)
	}
	return w.Flush()
}

// humanTime formats a timestamp as "X days ago" etc.
func humanTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "Just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// ── rmi ───────────────────────────────────────────────────────────────────────
// FIX #5 — removes manifest AND every layer file listed in it.

func cmdRmi(imageRef string) error {
	home, _ := os.UserHomeDir()
	if !strings.Contains(imageRef, ":") {
		imageRef += ":latest"
	}
	manifestPath := filepath.Join(home, ".docksmith", "images", imageRef+".json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("image %q not found", imageRef)
		}
		return err
	}

	var m struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("corrupt manifest: %w", err)
	}

	// Delete every layer file referenced by this manifest.
	layersDir := filepath.Join(home, ".docksmith", "layers")
	for _, layer := range m.Layers {
		hash := strings.TrimPrefix(layer.Digest, "sha256:")
		if hash == "" {
			continue
		}
		layerPath := filepath.Join(layersDir, hash+".tar")
		if err := os.Remove(layerPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove layer %s: %v\n", hash, err)
		} else {
			fmt.Printf("Deleted layer %s\n", hash[:12])
		}
	}

	// Delete the manifest.
	if err := os.Remove(manifestPath); err != nil {
		return fmt.Errorf("failed to remove manifest: %w", err)
	}
	fmt.Printf("Untagged: %s\n", imageRef)
	return nil
}

// ── usage ─────────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Print(`Docksmith — a simplified Docker-like build and runtime system

Usage:
  docksmith build -t <name:tag> [--no-cache] <context>
  docksmith run   [-e KEY=VALUE] <name:tag> [cmd ...]
  docksmith images
  docksmith rmi   <name:tag>
`)
}
