#!/usr/bin/env bash
# Lists every image (and OCI artifact) an installation of Bayplan pulls, for
# mirroring into a private registry. Read from the sources, not kept by hand:
# the chart's values, the embedded metrics-server manifest and the Trivy
# version pinned in the Containerfile.
#
#   deploy/airgap/images.sh 0.1.0 > bayplan-images.txt
#
# The CI attaches this list to every GitHub release.
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION="${1:?usage: images.sh <version>}"
VERSION="${VERSION#v}"

echo "ghcr.io/bayplan/bayplan:${VERSION}"
echo "ghcr.io/bayplan/bayplan-agent:${VERSION}"
# Bundled Postgres of the chart and of Compose/Quadlet.
sed -n 's/^  image: *\(.*postgres.*\)$/\1/p' deploy/helm/bayplan/values.yaml
# metrics-server, installable from a cluster's overview.
sed -n 's/^ *image: *\(.*metrics-server.*\)$/\1/p' backend/internal/k8s/metrics-server.yaml
# Trivy's vulnerability databases (OCI artifacts, not images): without them
# the image scan fails and says so.
echo "ghcr.io/aquasecurity/trivy-db:2"
echo "ghcr.io/aquasecurity/trivy-java-db:1"
