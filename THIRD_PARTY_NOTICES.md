# Third-party notices
Bayplan is distributed under the Apache 2.0 license (see `LICENSE`). The server binary, the agent binary and the
container images include the third-party software listed here, each under its own license. The
licenses and NOTICE files of each dependency are in its source code (Go modules in
`backend/go.sum`, npm packages in `frontend/package-lock.json`); the NOTICE files the Apache 2.0 license
requires to be kept are reproduced at the end. This file is regenerated with `make third-party`.

## Binaries and manifests included in the images
| Component | License | Source |
|---|---|---|
| kubectl (kubectl web shell) | Apache-2.0 | https://github.com/kubernetes/kubernetes |
| Trivy (image scanning) | Apache-2.0 | https://github.com/aquasecurity/trivy |
| metrics-server `components.yaml` (embedded installer, `backend/internal/k8s/metrics-server.yaml`) | Apache-2.0 | https://github.com/kubernetes-sigs/metrics-server |
| Alpine Linux base image | MIT (apk-tools) and the license of each package | https://alpinelinux.org |
| Inter font (`@fontsource-variable/inter`) | SIL Open Font License 1.1 | https://github.com/rsms/inter |

## Go modules (158, linked into `bayplan` and `bayplan-agent`)

Summary: Apache-2.0: 78, BSD-2-Clause: 6, BSD-3-Clause: 21, BSD-3-Clause OR MPL-2.0: 1, ISC: 2, MIT: 48, MPL-2.0: 2.

| Module | Version | License |
|---|---|---|
| cloud.google.com/go/compute/metadata | v0.3.0 | Apache-2.0 |
| dario.cat/mergo | v1.0.1 | BSD-3-Clause |
| github.com/BurntSushi/toml | v1.6.0 | MIT |
| github.com/MakeNowJust/heredoc | v1.0.0 | MIT |
| github.com/Masterminds/goutils | v1.1.1 | Apache-2.0 |
| github.com/Masterminds/semver/v3 | v3.5.0 | MIT |
| github.com/Masterminds/sprig/v3 | v3.3.0 | MIT |
| github.com/Masterminds/squirrel | v1.5.4 | MIT |
| github.com/ProtonMail/go-crypto | v1.4.1 | BSD-3-Clause |
| github.com/asaskevich/govalidator | v0.0.0-20230301143203-a9d515a09cc2 | MIT |
| github.com/aws/aws-sdk-go-v2 | v1.47.3 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream | v1.7.23 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/credentials | v1.20.9 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/internal/configsources | v1.5.6 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 | v2.8.6 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/internal/v4a | v1.5.6 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/eks | v1.104.2 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding | v1.13.21 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/internal/checksum | v1.11.7 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/internal/presigned-url | v1.14.6 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/internal/s3shared | v1.20.6 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/s3 | v1.114.4 | Apache-2.0 |
| github.com/aws/aws-sdk-go-v2/service/sts | v1.51.4 | Apache-2.0 |
| github.com/aws/smithy-go | v1.28.6 | Apache-2.0 |
| github.com/beorn7/perks | v1.0.1 | MIT |
| github.com/blang/semver/v4 | v4.0.0 | MIT |
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT |
| github.com/chai2010/gettext-go | v1.0.2 | BSD-3-Clause |
| github.com/cloudflare/circl | v1.6.3 | BSD-3-Clause |
| github.com/coder/websocket | v1.8.15 | ISC |
| github.com/coreos/go-oidc/v3 | v3.21.0 | Apache-2.0 |
| github.com/creack/pty | v1.1.24 | MIT |
| github.com/cyphar/filepath-securejoin | v0.7.0 | BSD-3-Clause OR MPL-2.0 |
| github.com/davecgh/go-spew | v1.1.2-0.20180830191138-d8f796af33cc | ISC |
| github.com/emicklei/go-restful/v3 | v3.13.0 | MIT |
| github.com/emirpasic/gods | v1.18.1 | BSD-2-Clause |
| github.com/evanphx/json-patch | v5.9.11+incompatible | BSD-3-Clause |
| github.com/exponent-io/jsonpath | v0.0.0-20210407135951-1de76d718b3f | MIT |
| github.com/fatih/color | v1.13.0 | MIT |
| github.com/fxamacker/cbor/v2 | v2.9.1 | MIT |
| github.com/go-errors/errors | v1.5.1 | MIT |
| github.com/go-git/gcfg | v1.5.1-0.20230307220236-3a3c6141e376 | BSD-3-Clause |
| github.com/go-git/go-billy/v5 | v5.9.2 | Apache-2.0 |
| github.com/go-git/go-git/v5 | v5.19.3 | Apache-2.0 |
| github.com/go-gorp/gorp/v3 | v3.1.0 | MIT |
| github.com/go-jose/go-jose/v4 | v4.1.6 | Apache-2.0 |
| github.com/go-logr/logr | v1.4.4 | Apache-2.0 |
| github.com/go-openapi/jsonpointer | v1.0.0 | Apache-2.0 |
| github.com/go-openapi/jsonreference | v1.0.0 | Apache-2.0 |
| github.com/go-openapi/swag | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/cmdutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/conv | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/fileutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/jsonutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/loading | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/mangling | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/netutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/pools | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/stringutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/typeutils | v0.27.1 | Apache-2.0 |
| github.com/go-openapi/swag/yamlutils | v0.27.1 | Apache-2.0 |
| github.com/gobwas/glob | v0.2.3 | MIT |
| github.com/golang/groupcache | v0.0.0-20241129210726-2c02b8208cf8 | Apache-2.0 |
| github.com/google/btree | v1.1.3 | Apache-2.0 |
| github.com/google/gnostic-models | v0.7.1 | Apache-2.0 |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/gorilla/websocket | v1.5.4-0.20250319132907-e064f32e3674 | BSD-2-Clause |
| github.com/gosuri/uitable | v0.0.4 | MIT |
| github.com/hashicorp/errwrap | v1.1.0 | MPL-2.0 |
| github.com/hashicorp/go-multierror | v1.1.1 | MPL-2.0 |
| github.com/huandu/xstrings | v1.5.0 | MIT |
| github.com/jackc/pgpassfile | v1.0.0 | MIT |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT |
| github.com/jackc/pgx/v5 | v5.11.0 | MIT |
| github.com/jackc/puddle/v2 | v2.2.3 | MIT |
| github.com/jbenet/go-context | v0.0.0-20150711004518-d14ea06fba99 | MIT |
| github.com/jmoiron/sqlx | v1.4.0 | MIT |
| github.com/json-iterator/go | v1.1.12 | MIT |
| github.com/kevinburke/ssh_config | v1.2.0 | MIT |
| github.com/lann/builder | v0.0.0-20180802200727-47ae307949d0 | MIT |
| github.com/lann/ps | v0.0.0-20150810152359-62de8c46ede0 | MIT |
| github.com/lib/pq | v1.12.3 | MIT |
| github.com/liggitt/tabwriter | v0.0.0-20181228230101-89fcab3d43de | BSD-3-Clause |
| github.com/mattn/go-colorable | v0.1.13 | MIT |
| github.com/mattn/go-isatty | v0.0.24 | MIT |
| github.com/mattn/go-runewidth | v0.0.9 | MIT |
| github.com/mfridman/interpolate | v0.0.2 | MIT |
| github.com/mitchellh/copystructure | v1.2.0 | MIT |
| github.com/mitchellh/go-wordwrap | v1.0.1 | MIT |
| github.com/mitchellh/reflectwalk | v1.0.2 | MIT |
| github.com/moby/spdystream | v0.5.1 | Apache-2.0 |
| github.com/moby/term | v0.5.2 | Apache-2.0 |
| github.com/modern-go/concurrent | v0.0.0-20180306012644-bacd9c7ef1dd | Apache-2.0 |
| github.com/modern-go/reflect2 | v1.0.3-0.20250322232337-35a7c28c31ee | Apache-2.0 |
| github.com/monochromegane/go-gitignore | v0.0.0-20200626010858-205db1a8cc00 | MIT |
| github.com/munnerz/goautoneg | v0.0.0-20191010083416-a7dc8b61c822 | BSD-3-Clause |
| github.com/opencontainers/go-digest | v1.0.0 | Apache-2.0 |
| github.com/opencontainers/image-spec | v1.1.1 | Apache-2.0 |
| github.com/peterbourgon/diskv | v2.0.1+incompatible | MIT |
| github.com/pjbgf/sha1cd | v0.7.0 | Apache-2.0 |
| github.com/pkg/errors | v0.9.1 | BSD-2-Clause |
| github.com/pmezard/go-difflib | v1.0.1-0.20181226105442-5d4384ee4fb2 | BSD-2-Clause |
| github.com/pressly/goose/v3 | v3.29.0 | MIT |
| github.com/prometheus/client_golang | v1.25.0 | Apache-2.0 |
| github.com/prometheus/client_model | v0.6.3 | Apache-2.0 |
| github.com/prometheus/common | v0.72.0 | Apache-2.0 |
| github.com/prometheus/procfs | v0.22.0 | Apache-2.0 |
| github.com/rancher/remotedialer | v0.6.1 | Apache-2.0 |
| github.com/rubenv/sql-migrate | v1.8.1 | MIT |
| github.com/russross/blackfriday/v2 | v2.1.0 | BSD-2-Clause |
| github.com/santhosh-tekuri/jsonschema/v6 | v6.0.3 | Apache-2.0 |
| github.com/sergi/go-diff | v1.4.0 | MIT |
| github.com/sethvargo/go-retry | v0.5.0 | Apache-2.0 |
| github.com/shopspring/decimal | v1.5.0 | MIT |
| github.com/sirupsen/logrus | v1.10.2 | MIT |
| github.com/skeema/knownhosts | v1.3.1 | Apache-2.0 |
| github.com/spf13/cast | v1.7.0 | MIT |
| github.com/spf13/cobra | v1.10.2 | Apache-2.0 |
| github.com/spf13/pflag | v1.0.10 | BSD-3-Clause |
| github.com/x448/float16 | v0.8.4 | MIT |
| github.com/xanzy/ssh-agent | v0.3.3 | Apache-2.0 |
| github.com/xlab/treeprint | v1.2.0 | MIT |
| go.uber.org/multierr | v1.11.0 | MIT |
| go.yaml.in/yaml/v2 | v2.4.4 | Apache-2.0 |
| go.yaml.in/yaml/v3 | v3.0.5 | Apache-2.0 |
| golang.org/x/crypto | v0.58.0 | BSD-3-Clause |
| golang.org/x/net | v0.61.0 | BSD-3-Clause |
| golang.org/x/oauth2 | v0.37.0 | BSD-3-Clause |
| golang.org/x/sync | v0.24.0 | BSD-3-Clause |
| golang.org/x/sys | v0.49.0 | BSD-3-Clause |
| golang.org/x/term | v0.47.0 | BSD-3-Clause |
| golang.org/x/text | v0.43.0 | BSD-3-Clause |
| golang.org/x/time | v0.16.0 | BSD-3-Clause |
| google.golang.org/protobuf | v1.36.12 | BSD-3-Clause |
| gopkg.in/evanphx/json-patch.v4 | v4.13.0 | BSD-3-Clause |
| gopkg.in/inf.v0 | v0.9.1 | BSD-3-Clause |
| gopkg.in/warnings.v0 | v0.1.2 | BSD-2-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | Apache-2.0 |
| helm.sh/helm/v3 | v3.22.1 | Apache-2.0 |
| k8s.io/api | v0.37.1 | Apache-2.0 |
| k8s.io/apiextensions-apiserver | v0.37.1 | Apache-2.0 |
| k8s.io/apimachinery | v0.37.1 | Apache-2.0 |
| k8s.io/apiserver | v0.37.1 | Apache-2.0 |
| k8s.io/cli-runtime | v0.37.1 | Apache-2.0 |
| k8s.io/client-go | v0.37.1 | Apache-2.0 |
| k8s.io/component-base | v0.37.1 | Apache-2.0 |
| k8s.io/klog/v2 | v2.140.0 | Apache-2.0 |
| k8s.io/kube-openapi | v0.0.0-20260721132016-d427ff9ee9ad | Apache-2.0 |
| k8s.io/kubectl | v0.37.1 | Apache-2.0 |
| k8s.io/streaming | v0.37.1 | Apache-2.0 |
| k8s.io/utils | v0.0.0-20260626114624-be93311217bd | Apache-2.0 |
| oras.land/oras-go/v2 | v2.6.2 | Apache-2.0 |
| sigs.k8s.io/json | v0.0.0-20250730193827-2d320260d730 | Apache-2.0 |
| sigs.k8s.io/kustomize/api | v0.21.2 | Apache-2.0 |
| sigs.k8s.io/kustomize/kyaml | v0.21.2 | Apache-2.0 |
| sigs.k8s.io/randfill | v1.0.0 | Apache-2.0 |
| sigs.k8s.io/structured-merge-diff/v6 | v6.4.2 | Apache-2.0 |
| sigs.k8s.io/yaml | v1.6.0 | Apache-2.0 |

## npm packages (168, bundled in the frontend)

Summary: 0BSD: 1, Apache-2.0: 2, BSD-3-Clause: 1, ISC: 9, MIT: 154, OFL-1.1: 1.

| Package | Version | License |
|---|---|---|
| @babel/runtime | 7.29.10 | MIT |
| @codemirror/autocomplete | 6.20.4 | MIT |
| @codemirror/commands | 6.11.1 | MIT |
| @codemirror/lang-yaml | 6.1.3 | MIT |
| @codemirror/language | 6.13.1 | MIT |
| @codemirror/lint | 6.9.7 | MIT |
| @codemirror/search | 6.7.2 | MIT |
| @codemirror/state | 6.7.6 | MIT |
| @codemirror/streamparser | 6.0.0 | MIT |
| @codemirror/theme-one-dark | 6.1.3 | MIT |
| @codemirror/view | 6.43.15 | MIT |
| @floating-ui/core | 1.8.0 | MIT |
| @floating-ui/dom | 1.8.0 | MIT |
| @floating-ui/react-dom | 2.1.9 | MIT |
| @floating-ui/utils | 0.2.12 | MIT |
| @fontsource-variable/inter | 5.3.0 | OFL-1.1 |
| @lezer/common | 1.5.4 | MIT |
| @lezer/highlight | 1.2.5 | MIT |
| @lezer/lr | 1.4.11 | MIT |
| @lezer/yaml | 1.0.4 | MIT |
| @marijn/find-cluster-break | 1.0.4 | MIT |
| @radix-ui/number | 1.1.3 | MIT |
| @radix-ui/primitive | 1.2.0 | MIT |
| @radix-ui/react-accessible-icon | 1.2.0 | MIT |
| @radix-ui/react-accordion | 1.3.1 | MIT |
| @radix-ui/react-alert-dialog | 1.2.1 | MIT |
| @radix-ui/react-arrow | 1.1.17 | MIT |
| @radix-ui/react-aspect-ratio | 1.2.0 | MIT |
| @radix-ui/react-avatar | 1.3.1 | MIT |
| @radix-ui/react-checkbox | 1.4.1 | MIT |
| @radix-ui/react-collapsible | 1.2.1 | MIT |
| @radix-ui/react-collection | 1.2.0 | MIT |
| @radix-ui/react-compose-refs | 1.1.5 | MIT |
| @radix-ui/react-context | 1.3.0 | MIT |
| @radix-ui/react-context-menu | 2.4.1 | MIT |
| @radix-ui/react-dialog | 1.3.1 | MIT |
| @radix-ui/react-direction | 1.2.0 | MIT |
| @radix-ui/react-dismissable-layer | 1.2.1 | MIT |
| @radix-ui/react-dropdown-menu | 2.2.1 | MIT |
| @radix-ui/react-focus-guards | 1.2.0 | MIT |
| @radix-ui/react-focus-scope | 1.3.1 | MIT |
| @radix-ui/react-form | 0.3.0 | MIT |
| @radix-ui/react-hover-card | 1.2.1 | MIT |
| @radix-ui/react-id | 1.1.4 | MIT |
| @radix-ui/react-label | 2.1.17 | MIT |
| @radix-ui/react-menu | 2.2.1 | MIT |
| @radix-ui/react-menubar | 1.2.1 | MIT |
| @radix-ui/react-navigation-menu | 1.4.1 | MIT |
| @radix-ui/react-one-time-password-field | 0.2.1 | MIT |
| @radix-ui/react-password-toggle-field | 0.2.1 | MIT |
| @radix-ui/react-popover | 1.3.1 | MIT |
| @radix-ui/react-popper | 1.4.0 | MIT |
| @radix-ui/react-portal | 1.2.0 | MIT |
| @radix-ui/react-presence | 1.1.11 | MIT |
| @radix-ui/react-primitive | 2.2.0 | MIT |
| @radix-ui/react-progress | 1.2.1 | MIT |
| @radix-ui/react-radio-group | 1.5.1 | MIT |
| @radix-ui/react-roving-focus | 1.2.1 | MIT |
| @radix-ui/react-scroll-area | 1.4.0 | MIT |
| @radix-ui/react-select | 2.4.1 | MIT |
| @radix-ui/react-separator | 1.2.0 | MIT |
| @radix-ui/react-slider | 1.6.1 | MIT |
| @radix-ui/react-slot | 1.5.0 | MIT |
| @radix-ui/react-switch | 1.4.1 | MIT |
| @radix-ui/react-tabs | 1.2.1 | MIT |
| @radix-ui/react-toast | 1.3.1 | MIT |
| @radix-ui/react-toggle | 1.2.1 | MIT |
| @radix-ui/react-toggle-group | 1.2.1 | MIT |
| @radix-ui/react-toolbar | 1.2.1 | MIT |
| @radix-ui/react-tooltip | 1.4.1 | MIT |
| @radix-ui/react-use-callback-ref | 1.1.5 | MIT |
| @radix-ui/react-use-controllable-state | 1.3.0 | MIT |
| @radix-ui/react-use-effect-event | 0.0.5 | MIT |
| @radix-ui/react-use-escape-keydown | 1.1.6 | MIT |
| @radix-ui/react-use-is-hydrated | 0.1.3 | MIT |
| @radix-ui/react-use-layout-effect | 1.1.4 | MIT |
| @radix-ui/react-use-previous | 1.1.4 | MIT |
| @radix-ui/react-use-rect | 1.1.4 | MIT |
| @radix-ui/react-use-size | 1.1.5 | MIT |
| @radix-ui/react-visually-hidden | 1.2.13 | MIT |
| @radix-ui/rect | 1.1.3 | MIT |
| @tanstack/query-core | 5.104.5 | MIT |
| @tanstack/react-query | 5.104.5 | MIT |
| @types/d3-color | 3.1.3 | MIT |
| @types/d3-drag | 3.0.7 | MIT |
| @types/d3-interpolate | 3.0.4 | MIT |
| @types/d3-selection | 3.0.12 | MIT |
| @types/d3-transition | 3.0.9 | MIT |
| @types/d3-zoom | 3.0.9 | MIT |
| @types/react | 19.3.0 | MIT |
| @types/react-dom | 19.3.0 | MIT |
| @typescript/typescript-linux-x64 | 7.0.2 | Apache-2.0 |
| @uiw/codemirror-extensions-basic-setup | 4.25.12 | MIT |
| @uiw/react-codemirror | 4.25.12 | MIT |
| @xterm/addon-fit | 0.11.0 | MIT |
| @xterm/xterm | 6.0.0 | MIT |
| @xyflow/react | 12.12.0 | MIT |
| @xyflow/system | 0.0.83 | MIT |
| agent-base | 6.0.2 | MIT |
| aria-hidden | 1.2.6 | MIT |
| asynckit | 0.4.0 | MIT |
| axios | 1.20.0 | MIT |
| call-bind-apply-helpers | 1.0.2 | MIT |
| classcat | 5.0.5 | MIT |
| clsx | 2.1.1 | MIT |
| codemirror | 6.0.2 | MIT |
| combined-stream | 1.0.8 | MIT |
| cookie | 1.1.1 | MIT |
| crelt | 1.0.7 | MIT |
| csstype | 3.2.3 | MIT |
| d3-color | 3.1.0 | ISC |
| d3-dispatch | 3.0.1 | ISC |
| d3-drag | 3.0.0 | ISC |
| d3-ease | 3.0.1 | BSD-3-Clause |
| d3-interpolate | 3.0.1 | ISC |
| d3-selection | 3.0.0 | ISC |
| d3-timer | 3.0.1 | ISC |
| d3-transition | 3.0.1 | ISC |
| d3-zoom | 3.0.0 | ISC |
| debug | 4.4.3 | MIT |
| delayed-stream | 1.0.0 | MIT |
| detect-node-es | 1.1.0 | MIT |
| dunder-proto | 1.0.1 | MIT |
| es-define-property | 1.0.1 | MIT |
| es-errors | 1.3.0 | MIT |
| es-object-atoms | 1.1.2 | MIT |
| es-set-tostringtag | 2.1.0 | MIT |
| follow-redirects | 1.16.1 | MIT |
| form-data | 4.0.6 | MIT |
| function-bind | 1.1.2 | MIT |
| get-intrinsic | 1.3.0 | MIT |
| get-nonce | 1.0.1 | MIT |
| get-proto | 1.0.1 | MIT |
| gopd | 1.2.0 | MIT |
| has-symbols | 1.1.0 | MIT |
| has-tostringtag | 1.0.2 | MIT |
| hasown | 2.0.4 | MIT |
| html-parse-stringify | 4.0.1 | MIT |
| https-proxy-agent | 5.0.1 | MIT |
| i18next | 26.5.6 | MIT |
| i18next-browser-languagedetector | 8.2.1 | MIT |
| i18next-resources-to-backend | 1.2.3 | MIT |
| lucide-react | 1.61.0 | ISC |
| math-intrinsics | 1.1.0 | MIT |
| mime-db | 1.52.0 | MIT |
| mime-types | 2.1.35 | MIT |
| ms | 2.1.3 | MIT |
| proxy-from-env | 2.1.0 | MIT |
| radix-ui | 1.8.1 | MIT |
| react | 19.3.1 | MIT |
| react-dom | 19.3.1 | MIT |
| react-i18next | 17.1.4 | MIT |
| react-remove-scroll | 2.7.2 | MIT |
| react-remove-scroll-bar | 2.3.8 | MIT |
| react-router | 7.18.4 | MIT |
| react-router-dom | 7.18.4 | MIT |
| react-style-singleton | 2.2.3 | MIT |
| scheduler | 0.28.0 | MIT |
| set-cookie-parser | 2.7.2 | MIT |
| style-mod | 4.1.4 | MIT |
| tailwind-merge | 3.7.0 | MIT |
| tslib | 2.8.1 | 0BSD |
| typescript | 7.0.2 | Apache-2.0 |
| use-callback-ref | 1.3.3 | MIT |
| use-sidecar | 1.1.3 | MIT |
| use-sync-external-store | 1.7.0 | MIT |
| w3c-keyname | 2.2.8 | MIT |
| zustand | 4.5.7 | MIT |

## Reproduced NOTICE files

### github.com/aws/aws-sdk-go-v2

```
AWS SDK for Go
Copyright 2015 Amazon.com, Inc. or its affiliates. All Rights Reserved.
Copyright 2014-2015 Stripe, Inc.
```

### github.com/aws/smithy-go

```
Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
```

### github.com/coreos/go-oidc/v3

```
CoreOS Project
Copyright 2014 CoreOS, Inc

This product includes software developed at CoreOS, Inc.
(http://www.coreos.com/).
```

### github.com/go-openapi/jsonpointer

```
Copyright 2015-2025 go-swagger maintainers

// SPDX-FileCopyrightText: Copyright 2015-2025 go-swagger maintainers
// SPDX-License-Identifier: Apache-2.0

This software library, github.com/go-openapi/jsonpointer, includes software developed
by the go-swagger and go-openapi maintainers ("go-swagger maintainers").

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this software except in compliance with the License.

You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0.

This software is copied from, derived from, and inspired by other original software products.
It ships with copies of other software which license terms are recalled below.

The original software was authored on 25-02-2013 by sigu-399 (https://github.com/sigu-399, sigu.399@gmail.com).

github.com/sigu-399/jsonpointer
===========================

// SPDX-FileCopyrightText: Copyright 2013 sigu-399 ( https://github.com/sigu-399 )
// SPDX-License-Identifier: Apache-2.0

Copyright 2013 sigu-399 ( https://github.com/sigu-399 )

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### github.com/go-openapi/jsonreference

```
Copyright 2015-2025 go-swagger maintainers

// SPDX-FileCopyrightText: Copyright 2015-2025 go-swagger maintainers
// SPDX-License-Identifier: Apache-2.0

This software library, github.com/go-openapi/jsonreference, includes software developed
by the go-swagger and go-openapi maintainers ("go-swagger maintainers").

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this software except in compliance with the License.

You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0.

This software is copied from, derived from, and inspired by other original software products.
It ships with copies of other software which license terms are recalled below.

The original software was authored on 25-02-2013 by sigu-399 (https://github.com/sigu-399, sigu.399@gmail.com).

github.com/sigh-399/jsonreference
===========================

// SPDX-FileCopyrightText: Copyright 2013 sigu-399 ( https://github.com/sigu-399 )
// SPDX-License-Identifier: Apache-2.0

Copyright 2013 sigu-399 ( https://github.com/sigu-399 )

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### github.com/moby/spdystream

```
SpdyStream
Copyright 2014-2021 Docker Inc.

This product includes software developed at
Docker Inc. (https://www.docker.com/).

SPDY implementation (spdy/)

The spdy directory contains code derived from the Go project (golang.org/x/net).

Copyright 2009-2013 The Go Authors.
Licensed under the BSD 3-Clause License.

Modifications Copyright 2014-2021 Docker Inc.

The BSD license text and Go patent grant are included in
spdy/LICENSE and spdy/PATENTS.
```

### github.com/prometheus/client_golang

```
Prometheus instrumentation library for Go applications
Copyright 2012-2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (http://soundcloud.com/).


The following components are included in this product:

perks - a fork of https://github.com/bmizerany/perks
https://github.com/beorn7/perks
Copyright 2013-2015 Blake Mizerany, Björn Rabenstein
See https://github.com/beorn7/perks/blob/master/README.md for license details.

Go support for Protocol Buffers - Google's data interchange format
http://github.com/golang/protobuf/
Copyright 2010 The Go Authors
See source code for license details.
```

### github.com/prometheus/client_model

```
Data model artifacts for Prometheus.
Copyright 2012-2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (http://soundcloud.com/).
```

### github.com/prometheus/common

```
Common libraries shared by Prometheus Go components.
Copyright 2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (http://soundcloud.com/).
```

### github.com/prometheus/procfs

```
procfs provides functions to retrieve system, kernel and process
metrics from the pseudo-filesystem proc.

Copyright 2014-2015 The Prometheus Authors

This product includes software developed at
SoundCloud Ltd. (http://soundcloud.com/).
```

### github.com/skeema/knownhosts

```
Copyright 2025 Skeema LLC and the Skeema Knownhosts authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### go.yaml.in/yaml/v2

```
Copyright 2011-2016 Canonical Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### go.yaml.in/yaml/v3

```
Copyright 2011-2016 Canonical Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### gopkg.in/yaml.v3

```
Copyright 2011-2016 Canonical Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### sigs.k8s.io/randfill

```
When donating the randfill project to the CNCF, we could not reach all the
gofuzz contributors to sign the CNCF CLA. As such, according to the CNCF rules
to donate a repository, we must add a NOTICE referencing section 7 of the CLA
with a list of developers who could not be reached.

`7. Should You wish to submit work that is not Your original creation, You may
submit it to the Foundation separately from any Contribution, identifying the
complete details of its source and of any license or other restriction
(including, but not limited to, related patents, trademarks, and license
agreements) of which you are personally aware, and conspicuously marking the
work as "Submitted on behalf of a third-party: [named here]".`

Submitted on behalf of a third-party: @dnephin (Daniel Nephin)
Submitted on behalf of a third-party: @AlekSi (Alexey Palazhchenko)
Submitted on behalf of a third-party: @bbigras (Bruno Bigras)
Submitted on behalf of a third-party: @samirkut (Samir)
Submitted on behalf of a third-party: @posener (Eyal Posener)
Submitted on behalf of a third-party: @Ashikpaul (Ashik Paul)
Submitted on behalf of a third-party: @kwongtailau (Kwongtai)
Submitted on behalf of a third-party: @ericcornelissen (Eric Cornelissen)
Submitted on behalf of a third-party: @eclipseo (Robert-André Mauchin)
Submitted on behalf of a third-party: @yanzhoupan (Andrew Pan)
Submitted on behalf of a third-party: @STRRL (Zhiqiang ZHOU)
Submitted on behalf of a third-party: @disconnect3d (Disconnect3d)
```
