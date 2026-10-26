#!/usr/bin/env bash
# Copies the images of a Bayplan version into a private registry, keeping their
# paths (ghcr.io/bayplan/bayplan -> REGISTRY/bayplan/bayplan), which is what the
# chart's imageRegistry value expects. All architectures are copied, and the
# Trivy databases too (they are OCI artifacts, which is why crane is preferred:
# skopeo only copies images).
#
#   docker login registry.example.com   # or crane auth login
#   deploy/airgap/mirror.sh registry.example.com 0.1.0
#
# Run it from a machine with access to both registries (or twice through a
# disk with `crane pull`/`crane push`).
set -euo pipefail
REGISTRY="${1:?usage: mirror.sh <registry[/prefix]> <version>}"
VERSION="${2:?usage: mirror.sh <registry[/prefix]> <version>}"
REGISTRY="${REGISTRY%/}"

if command -v crane >/dev/null; then
  copy() { crane copy --platform all "$1" "$2"; }
elif command -v skopeo >/dev/null; then
  copy() { skopeo copy --all "docker://$1" "docker://$2"; }
else
  echo "crane (github.com/google/go-containerregistry) or skopeo is required" >&2
  exit 1
fi

"$(dirname "$0")/images.sh" "$VERSION" | while read -r src; do
  [ -n "$src" ] || continue
  # Drop the source registry (the first segment if it looks like a host).
  path="$src"
  first="${src%%/*}"
  if [[ "$src" == */* && ( "$first" == *.* || "$first" == *:* || "$first" == localhost ) ]]; then
    path="${src#*/}"
  fi
  echo "==> $src -> $REGISTRY/$path"
  copy "$src" "$REGISTRY/$path"
done
