// Package build is the Docksmith build engine.
//
// Changes from original engine.go
// ────────────────────────────────
// FIX #1  isolation.RunShell replaces exec.Command for RUN
// FIX #2  delta layers: only changed/added files per layer
// FIX #3  CreateDeltaTar: sorted entries + zeroed timestamps → deterministic digests
// FIX #4  WORKDIR deferred: directory created just-in-time before COPY/RUN
// FIX #5  CLI (images/rmi) in cmd/main.go; engine exposes LoadImageManifest
// FIX #6  ENV override handled in runtime/container.go
// FIX #7  offline: CLONE_NEWNET inside isolation.RunShell
// FIX #8  base images loaded from ~/.docksmith/images; no downloads
// SPEC    created timestamp preserved on full-cache-hit rebuilds
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docksmith/isolation"
	"docksmith/parser"
	"docksmith/storage"
)

// ── Manifest types ────────────────────────────────────────────────────────────

// ImageConfig holds the runtime configuration stored in a manifest.
type ImageConfig struct {
	Env        []string `json:"Env"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
}

// LayerEntry describes one layer inside a manifest.
type LayerEntry struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

// Manifest is the JSON document written to ~/.docksmith/images/<name:tag>.json.
type Manifest struct {
	Name    string       `json:"name"`
	Tag     string       `json:"tag"`
	Digest  string       `json:"digest"`
	Created string       `json:"created"`
	Config  ImageConfig  `json:"config"`
	Layers  []LayerEntry `json:"layers"`
}

// ── Build options ─────────────────────────────────────────────────────────────

// BuildOptions controls a single build invocation.
type BuildOptions struct {
	Tag     string
	Context string
	NoCache bool
}

// ── Main build loop ───────────────────────────────────────────────────────────

// Run executes all instructions and writes the final image manifest.
func Run(instructions []parser.Instruction, opts BuildOptions) error {
	name, tag := parseTag(opts.Tag)

	workDir := ""
	pendingWorkDir := "" // FIX #4: not mkdir'd until next COPY or RUN
	envMap := map[string]string{}
	var layers []LayerEntry
	var cmdDefault []string
	prevLayerDigest := ""

	buildRoot, err := os.MkdirTemp("", "docksmith-build-*")
	if err != nil {
		return fmt.Errorf("failed to create build root: %w", err)
	}
	defer os.RemoveAll(buildRoot)

	cacheIndex := loadCacheIndex()
	cacheMissed := opts.NoCache

	// Spec: "When all steps are cache hits, the manifest is rewritten with the
	// original created value so the manifest digest remains identical across
	// rebuilds on the same machine."
	createdAt := time.Now().UTC().Format(time.RFC3339)
	existingCreatedAt := ""
	if existing, err := loadImageManifest(opts.Tag); err == nil {
		existingCreatedAt = existing.Created
	}
	allHits := !opts.NoCache // flipped to false on first cache miss

	total := len(instructions)

	for i, instr := range instructions {
		stepNum := i + 1

		switch instr.Type {

		// ── FROM ─────────────────────────────────────────────────────────────
		case "FROM":
			baseRef := instr.Args[0]
			fmt.Printf("Step %d/%d : FROM %s\n", stepNum, total, baseRef)

			if baseRef == "scratch" {
				prevLayerDigest = ""
				break
			}

			// FIX #8 — purely local; no downloads ever.
			baseManifest, err := loadImageManifest(baseRef)
			if err != nil {
				return fmt.Errorf("FROM: image %q not found in local store "+
					"(run scripts/import-base-image.sh first): %w", baseRef, err)
			}
			for _, layer := range baseManifest.Layers {
				hash := strings.TrimPrefix(layer.Digest, "sha256:")
				layerPath, err := storage.LoadLayer(hash)
				if err != nil {
					return fmt.Errorf("FROM: base layer %s missing: %w", hash[:12], err)
				}
				if err := storage.ExtractTar(layerPath, buildRoot); err != nil {
					return fmt.Errorf("FROM: extract failed: %w", err)
				}
			}
			layers = append(layers, baseManifest.Layers...)
			workDir = baseManifest.Config.WorkingDir
			pendingWorkDir = ""
			for _, kv := range baseManifest.Config.Env {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}
			prevLayerDigest = baseManifest.Digest

		// ── WORKDIR ──────────────────────────────────────────────────────────
		// FIX #4: store as pending; create the directory only when the next
		// layer-producing instruction (COPY or RUN) is about to execute.
		case "WORKDIR":
			workDir = instr.Args[0]
			pendingWorkDir = workDir
			fmt.Printf("Step %d/%d : WORKDIR %s\n", stepNum, total, workDir)

		// ── ENV ──────────────────────────────────────────────────────────────
		case "ENV":
			raw := strings.Join(instr.Args, " ")
			if idx := strings.Index(raw, "="); idx > 0 {
				envMap[raw[:idx]] = raw[idx+1:]
			} else if len(instr.Args) >= 2 {
				envMap[instr.Args[0]] = strings.Join(instr.Args[1:], " ")
			}
			fmt.Printf("Step %d/%d : ENV %s\n", stepNum, total, raw)

		// ── CMD ──────────────────────────────────────────────────────────────
		case "CMD":
			if len(instr.Args) == 1 && strings.HasPrefix(instr.Args[0], "[") {
				var parsed []string
				if err := json.Unmarshal([]byte(instr.Args[0]), &parsed); err == nil {
					cmdDefault = parsed
				} else {
					cmdDefault = instr.Args
				}
			} else {
				cmdDefault = instr.Args
			}
			fmt.Printf("Step %d/%d : CMD %v\n", stepNum, total, cmdDefault)

		// ── COPY ─────────────────────────────────────────────────────────────
		case "COPY":
			src := instr.Args[0]
			dest := instr.Args[1]
			createdBy := "COPY " + src + " " + dest
			fmt.Printf("Step %d/%d : COPY %s %s ", stepNum, total, src, dest)

			cacheKey, _ := computeCopyKey(prevLayerDigest, createdBy, workDir, envMap, opts.Context, src)

			if !cacheMissed && !opts.NoCache {
				if hit, ok := cacheIndex[cacheKey]; ok {
					if layerPath, err := storage.LoadLayer(hit); err == nil {
						fmt.Println("[CACHE HIT]")
						storage.ExtractTar(layerPath, buildRoot) //nolint:errcheck
						layers = append(layers, LayerEntry{
							Digest:    "sha256:" + hit,
							Size:      layerFileSize(hit),
							CreatedBy: createdBy,
						})
						prevLayerDigest = "sha256:" + hit
						continue
					}
				}
			}

			cacheMissed = true
			allHits = false
			start := time.Now()

			// FIX #4: create WORKDIR directory just in time.
			if err := ensureWorkDir(buildRoot, pendingWorkDir); err != nil {
				return err
			}
			pendingWorkDir = ""

			// FIX #2: snapshot filesystem before the instruction runs.
			baseline, err := snapshotBaseline(buildRoot)
			if err != nil {
				return fmt.Errorf("COPY baseline: %w", err)
			}

			if err := performCopy(opts.Context, src, buildRoot, dest, workDir); err != nil {
				os.RemoveAll(baseline)
				return fmt.Errorf("COPY failed: %w", err)
			}

			// FIX #2 + #3: save only the delta, sorted + zero timestamps.
			hash, err := saveDeltaLayer(buildRoot, baseline)
			os.RemoveAll(baseline)
			if err != nil {
				return fmt.Errorf("COPY saveDeltaLayer: %w", err)
			}

			if !opts.NoCache {
				cacheIndex[cacheKey] = hash
				saveCacheIndex(cacheIndex)
			}
			fmt.Printf("[CACHE MISS] %.2fs\n", time.Since(start).Seconds())
			layers = append(layers, LayerEntry{
				Digest:    "sha256:" + hash,
				Size:      layerFileSize(hash),
				CreatedBy: createdBy,
			})
			prevLayerDigest = "sha256:" + hash

		// ── RUN ──────────────────────────────────────────────────────────────
		case "RUN":
			command := strings.Join(instr.Args, " ")
			createdBy := "RUN " + command
			fmt.Printf("Step %d/%d : RUN %s ", stepNum, total, command)

			cacheKey := computeRunKey(prevLayerDigest, command, workDir, envMap)

			if !cacheMissed && !opts.NoCache {
				if hit, ok := cacheIndex[cacheKey]; ok {
					if layerPath, err := storage.LoadLayer(hit); err == nil {
						fmt.Println("[CACHE HIT]")
						storage.ExtractTar(layerPath, buildRoot) //nolint:errcheck
						layers = append(layers, LayerEntry{
							Digest:    "sha256:" + hit,
							Size:      layerFileSize(hit),
							CreatedBy: createdBy,
						})
						prevLayerDigest = "sha256:" + hit
						continue
					}
				}
			}

			cacheMissed = true
			allHits = false
			start := time.Now()

			// FIX #4: create WORKDIR directory just in time.
			if err := ensureWorkDir(buildRoot, pendingWorkDir); err != nil {
				return err
			}
			pendingWorkDir = ""

			// FIX #2: snapshot filesystem before the instruction runs.
			baseline, err := snapshotBaseline(buildRoot)
			if err != nil {
				return fmt.Errorf("RUN baseline: %w", err)
			}

			// FIX #1 + #7: namespace + chroot isolation, no outbound network.
			if err := isolation.RunShell(buildRoot, workDir, command, buildEnv(envMap)); err != nil {
				os.RemoveAll(baseline)
				return fmt.Errorf("RUN failed: %w", err)
			}

			// FIX #2 + #3: save only the delta, sorted + zero timestamps.
			hash, err := saveDeltaLayer(buildRoot, baseline)
			os.RemoveAll(baseline)
			if err != nil {
				return fmt.Errorf("RUN saveDeltaLayer: %w", err)
			}

			if !opts.NoCache {
				cacheIndex[cacheKey] = hash
				saveCacheIndex(cacheIndex)
			}
			fmt.Printf("[CACHE MISS] %.2fs\n", time.Since(start).Seconds())
			layers = append(layers, LayerEntry{
				Digest:    "sha256:" + hash,
				Size:      layerFileSize(hash),
				CreatedBy: createdBy,
			})
			prevLayerDigest = "sha256:" + hash

		default:
			return fmt.Errorf("unknown instruction %q", instr.Type)
		}
	}

	// Spec: on a full-cache-hit rebuild, restore the original created timestamp
	// so the manifest digest is stable across warm rebuilds.
	if allHits && existingCreatedAt != "" {
		createdAt = existingCreatedAt
	}

	manifest := Manifest{
		Name:    name,
		Tag:     tag,
		Created: createdAt,
		Config: ImageConfig{
			Env:        envMapToSlice(envMap),
			Cmd:        cmdDefault,
			WorkingDir: workDir,
		},
		Layers: layers,
	}

	digest, err := writeManifest(manifest)
	if err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}
	shortID := strings.TrimPrefix(digest, "sha256:")
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	fmt.Printf("Successfully built sha256:%s %s:%s\n", shortID, name, tag)
	return nil
}

// LoadImageManifest is the public entry point used by the runtime.
func LoadImageManifest(imageRef string) (*Manifest, error) {
	return loadImageManifest(imageRef)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// ensureWorkDir creates the pending WORKDIR path inside buildRoot.
// Called just before each COPY or RUN — never on the WORKDIR line itself.
func ensureWorkDir(buildRoot, pending string) error {
	if pending == "" {
		return nil
	}
	return os.MkdirAll(filepath.Join(buildRoot, pending), 0755)
}

// snapshotBaseline deep-copies buildRoot to a temp dir, preserving mtimes,
// so CreateDeltaTar can detect which files changed after the instruction ran.
func snapshotBaseline(buildRoot string) (string, error) {
	base, err := os.MkdirTemp("", "docksmith-baseline-*")
	if err != nil {
		return "", err
	}
	err = filepath.Walk(buildRoot, func(src string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(buildRoot, src)
		if rel == "." {
			return nil
		}
		dst := filepath.Join(base, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode())
		}
		return copyFilePreserve(src, dst, info)
	})
	if err != nil {
		os.RemoveAll(base)
		return "", err
	}
	return base, nil
}

// copyFilePreserve copies a file and sets the destination mtime to match the
// source, so baseline comparisons detect changes by mtime+size.
func copyFilePreserve(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// saveDeltaLayer creates a delta tar (FIX #2) with sorted+zeroed entries
// (FIX #3) and stores it in the layer store.
func saveDeltaLayer(buildRoot, baseline string) (string, error) {
	f, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return "", err
	}
	f.Close()

	if err := storage.CreateDeltaTar(buildRoot, baseline, f.Name()); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("CreateDeltaTar: %w", err)
	}
	hash, err := storage.SaveLayer(f.Name())
	if err != nil {
		return "", fmt.Errorf("SaveLayer: %w", err)
	}
	return hash, nil
}

// buildEnv produces the environment slice for an isolated process.
// A minimal PATH is set so shell built-ins work; image ENV vars are added.
func buildEnv(envMap map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	return env
}

func parseTag(tag string) (string, string) {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], "latest"
}

func envMapToSlice(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

func computeRunKey(prev, command, wd string, env map[string]string) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte("RUN " + command))
	h.Write([]byte(wd))
	h.Write([]byte(strings.Join(envMapToSlice(env), "\n")))
	return hex.EncodeToString(h.Sum(nil))
}

func computeCopyKey(prev, instr, wd string, env map[string]string, contextDir, src string) (string, error) {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte(instr))
	h.Write([]byte(wd))
	h.Write([]byte(strings.Join(envMapToSlice(env), "\n")))

	srcPath := filepath.Join(contextDir, src)
	var files []string
	filepath.Walk(srcPath, func(path string, info os.FileInfo, err error) error { //nolint:errcheck
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	for _, f := range files {
		fh, err := storage.ComputeSHA256(f)
		if err != nil {
			return "", err
		}
		h.Write([]byte(fh))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func performCopy(contextDir, src, buildRoot, dest, workDir string) error {
	srcPath := filepath.Join(contextDir, src)

	destPath := dest
	if !filepath.IsAbs(destPath) {
		destPath = filepath.Join(workDir, destPath)
	}
	destAbs := filepath.Join(buildRoot, destPath)

	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := os.MkdirAll(destAbs, 0755); err != nil {
			return err
		}
		return filepath.Walk(srcPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(srcPath, path)
			target := filepath.Join(destAbs, rel)
			if info.IsDir() {
				return os.MkdirAll(target, 0755)
			}
			os.MkdirAll(filepath.Dir(target), 0755) //nolint:errcheck
			return copyFile(path, target)
		})
	}

	if strings.HasSuffix(dest, "/") {
		os.MkdirAll(destAbs, 0755) //nolint:errcheck
		destAbs = filepath.Join(destAbs, filepath.Base(srcPath))
	} else {
		os.MkdirAll(filepath.Dir(destAbs), 0755) //nolint:errcheck
	}
	return copyFile(srcPath, destAbs)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func layerFileSize(hash string) int64 {
	path, err := storage.LoadLayer(hash)
	if err != nil {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// writeManifest serialises m to disk.
// The digest is computed over the JSON with digest="" (spec requirement),
// then the final file is written with the computed digest filled in.
func writeManifest(m Manifest) (string, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".docksmith", "images")
	os.MkdirAll(dir, 0755) //nolint:errcheck

	m.Digest = ""
	raw, _ := json.MarshalIndent(m, "", "  ")
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	m.Digest = digest
	final, _ := json.MarshalIndent(m, "", "  ")

	path := filepath.Join(dir, m.Name+":"+m.Tag+".json")
	os.WriteFile(path, final, 0644) //nolint:errcheck
	return digest, nil
}

func loadImageManifest(name string) (*Manifest, error) {
	home, _ := os.UserHomeDir()
	if !strings.Contains(name, ":") {
		name += ":latest"
	}
	file := filepath.Join(home, ".docksmith", "images", name+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var m Manifest
	json.Unmarshal(data, &m) //nolint:errcheck
	return &m, nil
}

func cacheIndexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docksmith", "cache", "index.json")
}

func loadCacheIndex() map[string]string {
	data, err := os.ReadFile(cacheIndexPath())
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	json.Unmarshal(data, &m) //nolint:errcheck
	return m
}

func saveCacheIndex(index map[string]string) {
	path := cacheIndexPath()
	os.MkdirAll(filepath.Dir(path), 0755) //nolint:errcheck
	data, _ := json.MarshalIndent(index, "", "  ")
	os.WriteFile(path, data, 0644) //nolint:errcheck
}