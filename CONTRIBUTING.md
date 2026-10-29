# Contributing

Thanks for wanting to improve Bayplan. The essentials:

## Before you start

- For large changes (a new feature, an architectural change) open an issue before writing
  code: that way the approach gets discussed and no work is lost.
- Read `docs/ARCHITECTURE.md`: it explains how the project is built and the decisions that
  should not be reverted.
- Vulnerabilities do NOT go through an issue or PR: see `SECURITY.md`.

## Development

```bash
make dev            # Postgres + local backend (see README, "Local development")
make lint           # gofmt + go vet + oxlint + locale keys
make test           # backend tests
cd frontend && npx tsc -b && npm run build
```

A PR must pass `make lint` and `make test`, and the frontend must compile with `npm run build`.
UI texts: new keys are added to `en.json` (the source of truth) and to every other locale under
`frontend/src/i18n/locales` (`es`, `ca`, `de`, `fr`, `pt-BR`, `zh-CN`, `ja`); `npm run lint`
checks that all locales have the same keys. Code comments are written in English and explain
the *why*. Database migrations are forward-only: an already applied migration is not edited,
another one is added.

## License of contributions and DCO

The project is under the Apache 2.0 license and every contribution is accepted under that same
license (section 5 of the license). There is no CLA: instead, the
[Developer Certificate of Origin](https://developercertificate.org/) (DCO) is used, which
certifies that you have the right to contribute that code. It is accepted by signing each
commit with `-s`:

```bash
git commit -s -m "Short summary of the change"
```

That adds a `Signed-off-by: Name <email>` line to the commit. PRs without sign-off are not
merged.

If your contribution includes third-party code, state it in the PR together with its license:
only software with a license compatible with Apache 2.0 (Apache, MIT, BSD, ISC, MPL 2.0…) is
accepted, never GPL/AGPL.

## Branches and pull requests

`main` is protected: nothing is pushed to it directly, not even by maintainers. Every change,
however small, goes through a branch and a pull request.

1. Branch off an up-to-date `main` with a short, descriptive name and one of these prefixes:
   `feat/…`, `fix/…`, `docs/…`, `refactor/…`, `chore/…` (dependencies, CI), `security/…`
   (only for fixes that are already public; see `SECURITY.md`). From a fork, the same.
2. Open the PR against `main`. CI must be green (lint, tests, third-party notices, secret
   scanning, chart and image scan) and every commit signed off (DCO).
3. PRs are merged with **squash**: one commit per PR on `main`, with the PR title as its
   summary, so `main` reads as a list of changes. The branch is deleted after merging.

### PR style

- One PR, one change. Refactors go separately from features.
- Title and description in English, explaining the problem it solves, not just the what.
- If something visible changes, a screenshot helps.
- If it changes something users notice, add a line under `[Unreleased]` in `CHANGELOG.md`.

## Releases

Releases are cut from `main` by a maintainer: the `[Unreleased]` section of `CHANGELOG.md` is
renamed to the version (in a PR like any other), and once merged a signed tag `vX.Y.Z` is pushed
on that commit. The tag is what publishes: CI builds and signs the images, the chart and the
SBOM and creates the GitHub release, whose notes are that version's `CHANGELOG.md` section.
Before publishing anything, CI checks that the tag matches `version` and `appVersion` in
`deploy/helm/bayplan/Chart.yaml` and that `CHANGELOG.md` has its section; if not, nothing is
published: fix it, delete the tag and push it again. A security fix for the latest version is
released as a patch (`vX.Y.Z+1`) the same way.
