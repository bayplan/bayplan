#!/usr/bin/env bash
# Regenerates THIRD_PARTY_NOTICES.md: Go modules actually linked into the server and the
# agent (go list -deps, not the whole go.mod) and production npm dependencies, with the license
# detected from each one's LICENSE file and the NOTICE files Apache 2.0 requires to be kept.
# Requires the Go modules downloaded (go mod download) and node_modules installed.
set -euo pipefail
cd "$(dirname "$0")/.."

cache=$(cd backend && go env GOMODCACHE)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

(cd backend && go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}} {{.Module.Version}}{{end}}{{end}}' ./cmd/server ./cmd/agent | LC_ALL=C sort -u) > "$tmp/gomods.txt"
(cd frontend && npm ls --omit=dev --all --json) > "$tmp/npm.json"

GOMODCACHE="$cache" TMP="$tmp" python3 - <<'PY'
import os, re, glob, json
from collections import Counter
cache = os.environ['GOMODCACHE']; tmp = os.environ['TMP']

def esc(p):  # go escapes uppercase letters in the module cache: Azure -> !azure
    return re.sub(r'[A-Z]', lambda m: '!' + m.group(0).lower(), p)

def classify(t):
    tl = t.lower()
    if 'mozilla public license' in tl: return 'MPL-2.0'
    if 'apache license' in tl and 'version 2.0' in tl: return 'Apache-2.0'
    if 'gnu affero' in tl: return 'AGPL-3.0'
    if 'gnu lesser' in tl: return 'LGPL'
    if 'gnu general public' in tl: return 'GPL'
    if 'permission is hereby granted, free of charge' in tl: return 'MIT'
    if 'redistribution and use in source and binary forms' in tl:
        return 'BSD-3-Clause' if 'neither the name' in tl else 'BSD-2-Clause'
    if 'permission to use, copy, modify, and/or distribute' in tl or 'permission to use, copy, modify, and distribute' in tl: return 'ISC'
    return 'OTHER'

# Dual-licensed modules: LICENSE.BSD + LICENSE.MPL-2.0 (the permissive one is chosen).
dual = {'github.com/cyphar/filepath-securejoin': 'BSD-3-Clause OR MPL-2.0'}

rows, notices, missing = [], [], []
for line in open(f'{tmp}/gomods.txt'):
    mod, ver = line.split()
    d = os.path.join(cache, esc(mod) + '@' + esc(ver))
    lic = None
    parts = mod.split('/')
    for cand in [d] + [os.path.join(cache, esc('/'.join(parts[:n])) + '@' + esc(ver)) for n in range(len(parts) - 1, 1, -1)]:
        fs = sorted(glob.glob(os.path.join(cand, 'LICENSE*')) + glob.glob(os.path.join(cand, 'LICENCE*')) + glob.glob(os.path.join(cand, 'COPYING*')))
        if fs: lic = fs[0]; break
    if mod in dual: rows.append((mod, ver, dual[mod]))
    elif not lic: missing.append(mod); rows.append((mod, ver, '?'))
    else: rows.append((mod, ver, classify(open(lic, errors='replace').read())))
    for nf in glob.glob(os.path.join(d, 'NOTICE*')):
        notices.append((mod, open(nf, errors='replace').read().strip()))

npm = json.load(open(f'{tmp}/npm.json'))
seen = {}
def walk(d):
    for n, v in (d.get('dependencies') or {}).items():
        # An optional peer nobody installed (zustand's immer) comes back as {}:
        # it is not in node_modules, so it is not shipped either.
        if 'version' not in v: continue
        seen.setdefault(n, v['version']); walk(v)
walk(npm)
nrows = []
for n, v in sorted(seen.items()):
    try:
        p = json.load(open(os.path.join('frontend/node_modules', n, 'package.json')))
        l = p.get('license'); l = l if isinstance(l, str) else (l or {}).get('type', '?')
    except Exception:
        l = '?'
    nrows.append((n, v, l))

bad = [r for r in rows + nrows if r[2] in ('?', 'OTHER', 'GPL', 'AGPL-3.0', 'LGPL')]
if bad or missing:
    raise SystemExit(f'Review by hand: {bad} {missing}')

cg, cn = Counter(r[2] for r in rows), Counter(r[2] for r in nrows)
out = ['# Third-party notices\n',
'Bayplan is distributed under the Apache 2.0 license (see `LICENSE`). The server binary, the agent binary and the\n'
'container images include the third-party software listed here, each under its own license. The\n'
'licenses and NOTICE files of each dependency are in its source code (Go modules in\n'
'`backend/go.sum`, npm packages in `frontend/package-lock.json`); the NOTICE files the Apache 2.0 license\n'
'requires to be kept are reproduced at the end. This file is regenerated with `make third-party`.\n',
'\n## Binaries and manifests included in the images\n',
'| Component | License | Source |\n|---|---|---|\n',
'| kubectl (kubectl web shell) | Apache-2.0 | https://github.com/kubernetes/kubernetes |\n',
'| Trivy (image scanning) | Apache-2.0 | https://github.com/aquasecurity/trivy |\n',
'| metrics-server `components.yaml` (embedded installer, `backend/internal/k8s/metrics-server.yaml`) | Apache-2.0 | https://github.com/kubernetes-sigs/metrics-server |\n',
'| Alpine Linux base image | MIT (apk-tools) and the license of each package | https://alpinelinux.org |\n',
'| Inter font (`@fontsource-variable/inter`) | SIL Open Font License 1.1 | https://github.com/rsms/inter |\n',
f'\n## Go modules ({len(rows)}, linked into `bayplan` and `bayplan-agent`)\n\n',
'Summary: ' + ', '.join(f'{k}: {v}' for k, v in sorted(cg.items())) + '.\n\n',
'| Module | Version | License |\n|---|---|---|\n']
out += [f'| {m} | {v} | {l} |\n' for m, v, l in rows]
out += [f'\n## npm packages ({len(nrows)}, bundled in the frontend)\n\n',
'Summary: ' + ', '.join(f'{k}: {v}' for k, v in sorted(cn.items())) + '.\n\n',
'| Package | Version | License |\n|---|---|---|\n']
out += [f'| {n} | {v} | {l} |\n' for n, v, l in nrows]
out.append('\n## Reproduced NOTICE files\n')
out += [f'\n### {m}\n\n```\n{t}\n```\n' for m, t in notices]
open('THIRD_PARTY_NOTICES.md', 'w').write(''.join(out))
print(f'THIRD_PARTY_NOTICES.md: {len(rows)} Go modules, {len(nrows)} npm packages, {len(notices)} NOTICE')
PY
