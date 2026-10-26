#!/usr/bin/env bash
# Looks for known vulnerabilities that have a fix, and for outdated pinned
# tools, and writes a Markdown report. The weekly security-scan workflow runs
# it against the PUBLISHED images (CVEs keep appearing after a release); `make
# audit` runs it locally against whatever images are passed.
#
#   scripts/security-scan.sh [image ...]        # report on stdout
#   REPORT=report.md scripts/security-scan.sh ghcr.io/bayplan/bayplan:latest
#
# Exit code 1 if something needs a patch release (a fixable HIGH/CRITICAL, a
# vulnerability govulncheck sees our code reach, an npm advisory of high
# severity); outdated tools only show up in the report.
#
# Needs go, npm, curl and trivy in the PATH (TRIVY=/path/to/trivy otherwise).
set -uo pipefail
cd "$(dirname "$0")/.."

TRIVY="${TRIVY:-trivy}"
GOVULNCHECK_VERSION=v1.9.0
REPORT="${REPORT:-/dev/stdout}"
found=0
out=$(mktemp)
trap 'rm -f "$out"' EXIT

section() { printf '\n## %s\n\n' "$1" >>"$out"; }

# The frontend is embedded with go:embed: without a dist directory the backend
# does not even compile (CI and fresh clones).
if [ ! -d backend/internal/web/dist ]; then
  mkdir -p backend/internal/web/dist
  echo '<!doctype html><title>placeholder</title>' >backend/internal/web/dist/index.html
fi

section "Go code (govulncheck)"
# `go run pkg@version` picks its toolchain from the tool, not from go.mod: on
# a machine whose system Go is older than the module's, govulncheck would be
# built with a Go that cannot load this code and the report would be empty.
# The toolchain the module asks for is forced, and a govulncheck that could not
# run (any exit code other than 0 = clean, 3 = vulnerabilities found) is
# reported as such instead of passing as "something has a fix".
GOVULN_TOOLCHAIN="go$(sed -n 's/^toolchain go//p; s/^go //p' backend/go.mod | head -1)"
set +e
(cd backend && GOTOOLCHAIN="$GOVULN_TOOLCHAIN" go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./... >/tmp/govulncheck.txt 2>&1)
govuln_rc=$?
set -e
case $govuln_rc in
  0) echo "No vulnerability reachable from the code." >>"$out" ;;
  3) found=1
     { echo '```'; grep -E '^(Vulnerability #|    [A-Z].*|  Module:|    (Found|Fixed) in:)' /tmp/govulncheck.txt; echo '```'; } >>"$out" ;;
  *) found=1
     { echo "govulncheck could NOT run (exit $govuln_rc); the Go code was not checked:"; echo '```'; tail -20 /tmp/govulncheck.txt; echo '```'; } >>"$out" ;;
esac

section "Frontend production dependencies (npm audit)"
if (cd frontend && npm audit --omit=dev --audit-level=high >/tmp/npm-audit.txt 2>&1); then
  echo "No advisory of high severity or above." >>"$out"
else
  found=1
  { echo '```'; grep -vE '^$' /tmp/npm-audit.txt | head -60; echo '```'; } >>"$out"
fi

section "Images (Trivy: HIGH/CRITICAL with a fix)"
if [ $# -eq 0 ]; then
  echo "No image given." >>"$out"
fi
for image in "$@"; do
  if ! "$TRIVY" image --quiet --image-src remote,docker --scanners vuln --severity HIGH,CRITICAL \
       --ignore-unfixed --ignorefile .trivyignore.yaml --format json --output /tmp/trivy.json "$image" 2>/tmp/trivy.err; then
    # Before the first release `latest` does not exist: say so instead of
    # passing it off as clean.
    reason=$(grep -v '^\s*$' /tmp/trivy.err | tail -1)
    echo "- \`$image\`: NOT scanned (${reason:-not published yet, or no access to the registry})" >>"$out"
    continue
  fi
  rows=$(jq -r '.Results[]? | .Target as $t | .Vulnerabilities[]? |
    "| \(.VulnerabilityID) | \(.Severity) | \(.PkgName) | \(.InstalledVersion) | \(.FixedVersion) | \($t) |"' /tmp/trivy.json | sort -u)
  if [ -z "$rows" ]; then
    echo "- \`$image\`: clean" >>"$out"
  else
    found=1
    { echo "- \`$image\`:"; echo; echo "| CVE | Severity | Package | Installed | Fixed | Where |"; echo "|---|---|---|---|---|---|"; echo "$rows"; echo; } >>"$out"
  fi
done

section "Pinned tools (deploy/Containerfile)"
pinned_kubectl=$(sed -n 's/^ARG KUBECTL_VERSION=//p' deploy/Containerfile)
pinned_trivy=$(sed -n 's/^ARG TRIVY_VERSION=//p' deploy/Containerfile)
latest_kubectl=$(curl -fsSL https://dl.k8s.io/release/stable.txt 2>/dev/null || echo unknown)
latest_trivy=$(curl -fsSL https://api.github.com/repos/aquasecurity/trivy/releases/latest 2>/dev/null | jq -r .tag_name | sed 's/^v//')
{
  echo "| Tool | Pinned | Latest |"
  echo "|---|---|---|"
  echo "| kubectl | $pinned_kubectl | $latest_kubectl |"
  echo "| trivy | $pinned_trivy | ${latest_trivy:-unknown} |"
} >>"$out"
if [ "$pinned_kubectl" != "$latest_kubectl" ] || [ "$pinned_trivy" != "$latest_trivy" ]; then
  printf '\nBump them in deploy/Containerfile (and TRIVY_VERSION/TRIVY_SHA256 in ci.yml).\n' >>"$out"
fi

{
  echo "# Bayplan security scan ($(date -u +%Y-%m-%d))"
  if [ "$found" -eq 1 ]; then
    echo
    echo "**Something has a fix available: publish a patch release** (see \"Upgrades and security patches\" in deploy/README.md)."
  fi
  cat "$out"
} >"$REPORT"
exit "$found"
