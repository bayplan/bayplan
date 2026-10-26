# Security policy

Bayplan manages cluster credentials and acts with the identity of whoever uses it, so a
vulnerability in the panel can be a vulnerability in every connected cluster. We take them
seriously.

## How to report

- Use GitHub's private advisories: **Security → Report a vulnerability** at
  https://github.com/bayplan/bayplan/security/advisories/new.
- Do not open a public issue or discuss it in a pull request until a fix has been published.
- Include the version (`/api/settings` → `serverVersion`, or the image tag), steps to reproduce
  and the impact you see. A working exploit is not required.

We respond within 7 days with a first assessment. If the problem is confirmed, it is fixed in
the next release and a security advisory (GHSA) is published crediting the reporter, if they
wish.

## Supported versions

Only the latest published version (the most recent `vX.Y.Z`) receives security fixes. Fixes ship
as a patch release (`vX.Y.Z+1`); the `latest` and `X.Y` image tags move to it, so installations
that follow those tags (Quadlet `AutoUpdate`, Compose with `latest`) pick it up on their next
pull. Upgrade instructions: "Upgrades and security patches" in `deploy/README.md`.

## Vulnerabilities in dependencies

Known vulnerabilities in dependencies (Go modules, npm packages, the Alpine base, PostgreSQL,
kubectl, Trivy) are tracked automatically: CI blocks publishing an image with a fixable HIGH or
CRITICAL finding, and a weekly scan of the published images opens a `security` issue when a new
one appears. There is no need to report them privately; an issue or a pull request is fine,
unless you have found that Bayplan exposes one in a way the scanners would not see.

## Scope

Everything this repository distributes is in scope: server, agent, Helm chart and container
images. Out of scope is what runs in the managed clusters (kubectl, Trivy, metrics-server and
the rest of the third-party software have their own channels) and insecure configurations
chosen by the operator that the documentation already advises against (for example
`insecureSkipTLSVerify`).
