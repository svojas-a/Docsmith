#!/usr/bin/env bash
# import-base-image.sh
#
# One-time setup script: downloads Alpine 3.18 and imports it into the
# Docksmith local image store (~/.docksmith/images + ~/.docksmith/layers).
#
# After this runs, all builds and runs work fully offline.
# Do NOT call this from inside a build or run.
#
# Usage:
#   chmod +x scripts/import-base-image.sh
#   ./scripts/import-base-image.sh

set -euo pipefail

DOCKSMITH_DIR="${HOME}/.docksmith"
IMAGES_DIR="${DOCKSMITH_DIR}/images"
LAYERS_DIR="${DOCKSMITH_DIR}/layers"
CACHE_DIR="${DOCKSMITH_DIR}/cache"
TMP=$(mktemp -d)

mkdir -p "${IMAGES_DIR}" "${LAYERS_DIR}" "${CACHE_DIR}"

echo "==> Fetching Alpine 3.18 layer list from registry..."

# Use the Docker Hub v2 API to get the manifest for alpine:3.18 (linux/amd64).
TOKEN=$(curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull" | \
  python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

MANIFEST=$(curl -s -H "Authorization: Bearer ${TOKEN}" \
  -H "Accept: application/vnd.docker.distribution.manifest.v2+json" \
  "https://registry-1.docker.io/v2/library/alpine/manifests/3.18")

CONFIG_DIGEST=$(echo "${MANIFEST}" | python3 -c "import sys,json; m=json.load(sys.stdin); print(m['config']['digest'])")
LAYER_DIGEST=$(echo "${MANIFEST}" | python3 -c "import sys,json; m=json.load(sys.stdin); print(m['layers'][0]['digest'])")

echo "    config: ${CONFIG_DIGEST}"
echo "    layer:  ${LAYER_DIGEST}"

# Download the single Alpine layer.
LAYER_HASH="${LAYER_DIGEST#sha256:}"
LAYER_PATH="${LAYERS_DIR}/${LAYER_HASH}.tar"

if [ -f "${LAYER_PATH}" ]; then
  echo "==> Layer already present, skipping download."
else
  echo "==> Downloading Alpine layer (~3 MB)..."
  curl -sL -H "Authorization: Bearer ${TOKEN}" \
    "https://registry-1.docker.io/v2/library/alpine/blobs/${LAYER_DIGEST}" \
    -o "${TMP}/alpine.tar.gz"

  # Docker layers are gzipped tarballs. Decompress to a plain tar.
  gunzip -c "${TMP}/alpine.tar.gz" > "${LAYER_PATH}"
  echo "==> Layer saved: ${LAYER_HASH:0:12}"
fi

# Compute actual SHA256 of the decompressed tar (Docksmith uses raw-tar hashes).
ACTUAL_HASH=$(sha256sum "${LAYER_PATH}" | awk '{print $1}')

# If the hash changed after decompression, rename the file.
if [ "${ACTUAL_HASH}" != "${LAYER_HASH}" ]; then
  mv "${LAYER_PATH}" "${LAYERS_DIR}/${ACTUAL_HASH}.tar"
  LAYER_HASH="${ACTUAL_HASH}"
  echo "==> Renamed layer to decompressed hash: ${LAYER_HASH:0:12}"
fi

LAYER_SIZE=$(stat -c%s "${LAYERS_DIR}/${LAYER_HASH}.tar")

# Build the Docksmith manifest for alpine:3.18.
CREATED=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Write manifest with digest="" first, compute hash, then write final.
MANIFEST_PATH="${IMAGES_DIR}/alpine:3.18.json"

python3 - <<PYEOF
import json, hashlib, os

manifest = {
    "name": "alpine",
    "tag": "3.18",
    "digest": "",
    "created": "${CREATED}",
    "config": {
        "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
        "Cmd": ["/bin/sh"],
        "WorkingDir": ""
    },
    "layers": [
        {
            "digest": "sha256:${LAYER_HASH}",
            "size": ${LAYER_SIZE},
            "createdBy": "alpine:3.18 base layer"
        }
    ]
}

canonical = json.dumps(manifest, indent=2).encode()
digest = "sha256:" + hashlib.sha256(canonical).hexdigest()
manifest["digest"] = digest

with open("${MANIFEST_PATH}", "w") as f:
    json.dump(manifest, f, indent=2)

print(f"==> Manifest written: alpine:3.18  digest={digest[:19]}")
PYEOF

rm -rf "${TMP}"
echo ""
echo "✅ alpine:3.18 is now available in the local Docksmith store."
echo "   You can now run: docksmith build -t myapp:latest ./sampleapp"