package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docksmith/parser"
	"docksmith/storage"
)

type ImageConfig struct {
	Env        []string `json:"Env"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
}

type LayerEntry struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

type Manifest struct {
	Name    string       `json:"name"`
	Tag     string       `json:"tag"`
	Digest  string       `json:"digest"`
	Created string       `json:"created"`
	Config  ImageConfig  `json:"config"`
	Layers  []LayerEntry `json:"layers"`
}

type BuildOptions struct {
	Tag     string
	Context string
	NoCache bool
}

func Run(instructions []parser.Instruction, opts BuildOptions) error {
	name, tag := parseTag(opts.Tag)

	workDir := ""
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
	createdAt := time.Now().UTC().Format(time.RFC3339)

	for i, instr := range instructions {
		stepNum := i + 1
		total := len(instructions)

		switch instr.Type {

		case "FROM":
			baseImage := instr.Args[0]
			fmt.Printf("Step %d/%d : FROM %s\n", stepNum, total, baseImage)

			// ✅ HANDLE SCRATCH (EMPTY BASE)
			if baseImage == "scratch" {
				fmt.Println("Using empty base image (scratch)")
				workDir = ""
				envMap = map[string]string{}
				prevLayerDigest = ""
				break
			}

			baseManifest, err := loadImageManifest(baseImage)
			if err != nil {
				return fmt.Errorf("FROM: image %q not found: %w", baseImage, err)
			}
			for _, layer := range baseManifest.Layers {
				hash := strings.TrimPrefix(layer.Digest, "sha256:")
				layerPath, err := storage.LoadLayer(hash)
				if err != nil {
					return fmt.Errorf("FROM: layer missing: %w", err)
				}
				if err := storage.ExtractTar(layerPath, buildRoot); err != nil {
					return fmt.Errorf("FROM: extract failed: %w", err)
				}
			}
			layers = append(layers, baseManifest.Layers...)
			workDir = baseManifest.Config.WorkingDir
			for _, kv := range baseManifest.Config.Env {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}
			prevLayerDigest = baseManifest.Digest
			createdAt = baseManifest.Created

		case "WORKDIR":
			workDir = instr.Args[0]
			fmt.Printf("Step %d/%d : WORKDIR %s\n", stepNum, total, workDir)
			os.MkdirAll(filepath.Join(buildRoot, workDir), 0755)

		case "ENV":
			kv := strings.Join(instr.Args, "=")
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				envMap[parts[0]] = parts[1]
			}
			fmt.Printf("Step %d/%d : ENV %s\n", stepNum, total, kv)

		case "CMD":

	        // FIX: unwrap JSON string if needed
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
						storage.ExtractTar(layerPath, buildRoot)
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
			start := time.Now()
			os.MkdirAll(filepath.Join(buildRoot, workDir), 0755)
			performCopy(opts.Context, src, buildRoot, dest, workDir)
			hash, _ := saveLayer(buildRoot)
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

		case "RUN":
			command := strings.Join(instr.Args, " ")
			createdBy := "RUN " + command
			fmt.Printf("Step %d/%d : RUN %s ", stepNum, total, command)

			cacheKey := computeRunKey(prevLayerDigest, command, workDir, envMap)

			if !cacheMissed && !opts.NoCache {
				if hit, ok := cacheIndex[cacheKey]; ok {
					if layerPath, err := storage.LoadLayer(hit); err == nil {
						fmt.Println("[CACHE HIT]")
						storage.ExtractTar(layerPath, buildRoot)
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
			start := time.Now()
			if err := executeRun(buildRoot, workDir, command, envMap); err != nil {
				return fmt.Errorf("RUN failed: %w", err)
			}
			fmt.Println("DEBUG: listing files after RUN")
			exec.Command("ls", "-R", filepath.Join(buildRoot, workDir)).Run()

			// ✅ After successful build → set executable permission
			// Make all binaries executable after RUN
			filepath.Walk(filepath.Join(buildRoot, workDir), func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}

				// If no extension → likely binary
				if filepath.Ext(path) == "" {
					os.Chmod(path, 0755)
				}
				return nil
			})
			hash, _ := saveLayer(buildRoot)
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
	fmt.Printf("Successfully built %s %s:%s\n", digest[:19], name, tag)
	return nil
}

func LoadImageManifest(imageRef string) (*Manifest, error) {
	return loadImageManifest(imageRef)
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
	filepath.Walk(srcPath, func(path string, info os.FileInfo, err error) error {
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

func executeRun(buildRoot, workDir, command string, envMap map[string]string) error {
	cmd := exec.Command("sh", "-c", command)

	// Set working directory inside buildRoot
	if workDir != "" {
		cmd.Dir = filepath.Join(buildRoot, workDir)
	} else {
		cmd.Dir = buildRoot
	}

	// Base environment
	cmd.Env = os.Environ()

	// 🔥 CRITICAL FIXES
	cmd.Env = append(cmd.Env,
		"HOME="+buildRoot,
		"GOPATH="+filepath.Join(buildRoot, "gopath"),
		"GOCACHE="+filepath.Join(buildRoot, "gocache"),
	)

	// Add ENV variables from Dockerfile
	for k, v := range envMap {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func performCopy(contextDir, src, buildRoot, dest, workDir string) error {

	// Resolve source path
	srcPath := filepath.Join(contextDir, src)

	// Resolve destination path inside container
	destPath := dest

	// If dest is relative → place inside WORKDIR
	if !filepath.IsAbs(destPath) {
		destPath = filepath.Join(workDir, destPath)
	}

	destAbs := filepath.Join(buildRoot, destPath)

	// Get source info
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	// Case 1: Source is a directory (e.g., COPY . .)
	if info.IsDir() {

		// Ensure destination exists
		if err := os.MkdirAll(destAbs, 0755); err != nil {
			return err
		}

		return filepath.Walk(srcPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(srcPath, path)
			if err != nil {
				return err
			}

			target := filepath.Join(destAbs, rel)

			if info.IsDir() {
				return os.MkdirAll(target, 0755)
			}

			// Ensure parent dir exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			return copyFile(path, target)
		})
	}

	// Case 2: Source is a file
	// If dest ends with "/" → treat as directory
	if strings.HasSuffix(dest, "/") {
		if err := os.MkdirAll(destAbs, 0755); err != nil {
			return err
		}
		destAbs = filepath.Join(destAbs, filepath.Base(srcPath))
	} else {
		// Ensure parent exists
		if err := os.MkdirAll(filepath.Dir(destAbs), 0755); err != nil {
			return err
		}
	}

	return copyFile(srcPath, destAbs)
}

func copyDirContents(srcDir, destDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." {
			return nil
		}
		target := filepath.Join(destDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		os.MkdirAll(filepath.Dir(target), 0755)
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Get source permissions
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination with SAME permissions
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func saveLayer(sourceDir string) (string, error) {
	f, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return "", fmt.Errorf("saveLayer tempfile: %w", err)
	}
	f.Close()
	if err := storage.CreateTar(sourceDir, f.Name()); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("saveLayer createtar: %w", err)
	}
	hash, err := storage.SaveLayer(f.Name())
	if err != nil {
		return "", fmt.Errorf("saveLayer savelayer: %w", err)
	}
	return hash, nil
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

func writeManifest(m Manifest) (string, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".docksmith", "images")
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, m.Name+":"+m.Tag+".json")

	m.Digest = ""
	raw, _ := json.MarshalIndent(m, "", "  ")
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	m.Digest = digest
	final, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(path, final, 0644)
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
	json.Unmarshal(data, &m)
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
	json.Unmarshal(data, &m)
	return m
}

func saveCacheIndex(index map[string]string) {
	path := cacheIndexPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, _ := json.MarshalIndent(index, "", "  ")
	os.WriteFile(path, data, 0644)
}
