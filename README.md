# Docksmith - neo (CLI + Parser)

## Completed Work

* Implemented CLI with `build` command
* Parsed Docksmithfile into structured instructions
* Added validation for instructions
* Supported `-t` flag for image tagging

## Build the Executable

```bash
go build -o docksmith cmd/main.go
```

## How to Run

```bash
./docksmith build -t myapp:latest .
```

## Output Example

Building image with tag: myapp:latest
Parsed Instructions:

1. FROM ubuntu
2. WORKDIR /app
3. COPY . .
4. RUN go build -o app
5. CMD ["./app"]

## For Svojas

Use this function:
parser.ParseDocksmithfile("Docksmithfile")

It returns structured instructions you can use to build layers.

## Storage & Layer System (Person 2)

Completed Work
	•	Initialized storage directory at ~/.docksmith/
	•	Implemented content-addressed layer storage
	•	Implemented tar-based layer creation
	•	Implemented layer extraction
	•	Implemented SHA256 hashing for files and layers
	•	Implemented deduplication (same layer is not stored twice)

## Storage Structure

All data is stored in:
~/.docksmith/
├── images/   # (to be used by next phase)
├── layers/   # stores layers as sha256.tar
├── cache/    # (to be used later)

## Core Concepts Implemented

🔹 Layers
	•	Each layer is a .tar archive of filesystem contents
	•	Stored using SHA256 hash of content

Example:
~/.docksmith/layers/<hash>.tar

🔹 Content Addressing
	•	Layer name = SHA256 hash of its content
	•	Same content → same hash → reused layer

Storage API (IMPORTANT FOR PERSON 3)


1. CreateTar
storage.CreateTar(sourceDir, tarPath)

	•	Creates a tar archive from any directory
	•	sourceDir → folder to archive
	•	tarPath → output tar file (temporary)


2. SaveLayer
hash, err := storage.SaveLayer(tarPath)

	•	Stores tar file in ~/.docksmith/layers/
	•	Uses SHA256 hash as filename
	•	Automatically avoids duplicates
	•	Moves (or renames) the tar file into storage


3. LoadLayer

path, err := storage.LoadLayer(hash)

	•	Returns full path of stored layer
	•	Used to retrieve layers later


4. ExtractTar

storage.ExtractTar(tarPath, destDir)
	•	Extracts tar archive into a directory
	•	Used for reconstructing filesystem


5. ComputeSHA256
hash, err := storage.ComputeSHA256(filePath)

	•	Computes SHA256 hash of any file
	•	Used internally for content addressing

    Example Flow (How Storage Works)

    Directory → CreateTar → layer.tar
layer.tar → SaveLayer → ~/.docksmith/layers/<hash>.tar
hash → LoadLayer → path to layer
layer.tar → ExtractTar → restored filesystem

## 🚀 For Person 3 (Build Engine)

You will use:
instructions, _ := parser.ParseDocksmithfile("Docksmithfile")

Then for each instruction:

tempDir := "/tmp/build"

// modify filesystem (COPY, RUN, etc.)

storage.CreateTar(tempDir, "layer.tar")
hash, _ := storage.SaveLayer("layer.tar")
