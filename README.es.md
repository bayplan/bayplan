
<p align="center">
  <img src="docs/brand/bayplan-logo.svg" alt="Bayplan" width="360">
</p>

# Bayplan

*Every container, in its place.*

<p align="center">
  <a href="https://github.com/bayplan/bayplan/actions/workflows/ci.yml"><img src="https://github.com/bayplan/bayplan/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/bayplan/bayplan/releases/latest"><img src="https://img.shields.io/github/v/release/bayplan/bayplan?sort=semver" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/bayplan/bayplan" alt="License: Apache-2.0"></a>
  <a href="https://github.com/bayplan/bayplan/pkgs/container/bayplan"><img src="https://img.shields.io/badge/image-ghcr.io%2Fbayplan%2Fbayplan-2f2f2f" alt="Container image"></a>
</p>

English version: [README.md](README.md)

Gestión multiclúster de Kubernetes, ligera y autoalojada: un único binario (Go + React
embebido) más Postgres, desplegado en la propia infraestructura de contenedores del cliente,
desde el que se observan y operan varios clústeres (on-prem, kubeadm, k3s, RKE2, Talos,
cloud…), con login local o SSO OIDC (Entra ID, Keycloak, Okta, Google…) y un modelo de permisos
por clúster con tres roles (`admin`, `editor`, `readonly`).

**Qué hace:** inventario y estado de cargas de trabajo, red, configuración y almacenamiento
desde una caché de informers por clúster; logs y terminal; restart, scale, rollback,
cordon/drain; editar y aplicar YAML con validación previa en el clúster (server-side apply con
dry-run); auditoría inmutable de todo. **Qué no hace:** aprovisionar clústeres ni actualizar su
versión de Kubernetes, ni es un controlador GitOps completo.

<p align="center">
  <img src="docs/screenshots/cluster-overview.png" alt="Vista general del clúster" width="900">
</p>

<table>
  <tr>
    <td><img src="docs/screenshots/dashboard.png" alt="Panel de la flota"></td>
    <td><img src="docs/screenshots/topology.png" alt="Topología: Ingress → Service → workload → pods → volúmenes"></td>
  </tr>
  <tr>
    <td align="center"><sub>Panel de la flota</sub></td>
    <td align="center"><sub>Topología: Ingress → Service → workload → pods → volúmenes</sub></td>
  </tr>
  <tr>
    <td><img src="docs/screenshots/pods.png" alt="Pods con consumo en vivo"></td>
    <td><img src="docs/screenshots/cluster-overview-dark.png" alt="Tema oscuro"></td>
  </tr>
  <tr>
    <td align="center"><sub>Pods con consumo en vivo</sub></td>
    <td align="center"><sub>Tema oscuro</sub></td>
  </tr>
</table>

## Instalación

Para probarlo, o en un único host, un solo contenedor con su propia base de datos y HTTPS:

```bash
docker run -d --name bayplan --restart=always -p 8443:8443 -v bayplan-data:/data ghcr.io/bayplan/bayplan
docker logs bayplan 2>&1 | grep -A3 "First startup"   # token de /setup de un solo uso; después abre https://<host>:8443
```

En Kubernetes, con Helm (el clúster donde se instala aparece como `local`; el resto se añade
desde la UI con un agente):

```bash
helm upgrade --install bayplan oci://ghcr.io/bayplan/charts/bayplan -n bayplan --create-namespace \
  --set ingress.enabled=true --set ingress.host=bayplan.example.com --set ingress.tls.enabled=true
kubectl -n bayplan get secret bayplan-bootstrap -o go-template='{{.data.bootstrapPassword|base64decode}}{{"\n"}}'
```

Con Postgres en su propio contenedor (Docker o Podman Compose; pon delante un proxy con TLS):

```bash
curl -fsSLO https://raw.githubusercontent.com/bayplan/bayplan/main/deploy/compose/docker-compose.yml
curl -fsSLo bayplan.env https://raw.githubusercontent.com/bayplan/bayplan/main/deploy/env.example
# edita bayplan.env: DATABASE_URL (contraseña), ENCRYPTION_KEY_V1 (openssl rand -base64 32), PUBLIC_URL
POSTGRES_PASSWORD=<la de DATABASE_URL> docker compose up -d
```

Imágenes para amd64 y arm64, firmadas con cosign. Opciones, Let's Encrypt, Podman Quadlet, GitOps,
instalaciones sin conexión a internet, copias de seguridad y actualizaciones:
[`deploy/README.md`](deploy/README.md).

## Funcionalidades

- **Autenticación**: login local (bcrypt, rate limit, cambio de contraseña forzado en el primer
  acceso) + SSO opcional con cualquier proveedor OIDC (Entra ID, Keycloak, Okta, Google…;
  Authorization Code + PKCE, configurado desde Ajustes). Sesiones en el servidor revocables,
  cookie `HttpOnly`, CSRF por double-submit.
- **RBAC por ámbito**: roles `admin`/`editor`/`readonly`, asignables de forma global o por
  clúster concreto (un usuario puede ser admin en staging y readonly en producción). Gestión de
  usuarios y roles desde la propia UI (`/admin/users`): crear, deshabilitar/rehabilitar (baja
  reversible, corta sus sesiones al instante) y borrar usuarios locales. El sistema nunca se
  queda sin un admin global activo: la última cuenta con ese rol no se puede borrar,
  deshabilitar ni degradar. El detalle de cada usuario muestra sus **permisos efectivos**: qué
  rol tiene realmente en cada clúster y **de dónde le viene** (rol global, asignación de clúster
  o ambas), que es lo que hay que saber para quitárselo.
- **Mapeo de grupos SSO** desde la UI (`/admin/group-mappings`): traduce la pertenencia a un
  grupo del proveedor de identidad en un rol de Bayplan. Junto con las asignaciones manuales es
  la única fuente de permisos de los usuarios SSO, así que la pantalla avisa si el SSO está
  activo y no hay ningún mapeo.
- **Multiclúster**: cada clúster gestionado tiene su propio `SharedInformerFactory` en el
  backend — las lecturas (pods, deployments, statefulsets, daemonsets, services, nodos,
  eventos) se sirven desde una caché en memoria, sin golpear el API server del clúster en cada
  petición.
- **Escaneo de vulnerabilidades** de las imágenes en uso con Trivy (en el servidor), por imagen
  y compartido entre clústeres, con un informe filtrable por severidad.
- **Extensiones de UI**: enlaces a Grafana, Kibana, Harbor… en el menú de cada clúster, en
  otra pestaña o embebidos, con placeholders de clúster y namespace en la URL.
- **Clúster local sin configuración**: el clúster donde se instala aparece como `local` sin
  configurar nada; los demás entran mediante un agente (manifiesto generado) que mantiene al día
  su propio RBAC y su versión; las conexiones directa (kubeconfig/token/certificado) y cloud
  (EKS/AKS/GKE) siguen disponibles.
- **Permisos a elegir**: RBAC acotado por defecto (el panel solo lee lo que muestra y escribe a
  través de tres roles) o `cluster-admin` opcional para instalar y olvidarse. metrics-server se
  puede instalar desde el panel si falta.
- **Coste estimado por namespace** a partir de precios de núcleo·hora y GiB·hora: reservado,
  consumido (con metrics-server) y facturable (el mayor de los dos por recurso). Es un reparto,
  no una factura.
- **Copias de seguridad** de la configuración del clúster en S3 (AWS, MinIO, Ceph…),
  programadas y con retención, restaurables objeto a objeto con dry-run. No copian volúmenes
  (Velero).
- **API con tokens personales**: cada usuario crea tokens (`Authorization: Bearer bpl_…`,
  opcionalmente de solo lectura) para scripts y CI; especificación OpenAPI en
  `/api/openapi.json` y explorador en `/api-docs`.
- **Explicar con IA**: un clic en un pod, deployment o nodo con fallos envía su estado, sus
  eventos y las últimas líneas de log (sin valores de variables de entorno ni secretos) al
  proveedor que configures — OpenAI o cualquier servidor compatible como Ollama, Anthropic o
  Gemini — y muestra la causa probable y la solución. Rol mínimo, límite diario por persona y
  auditoría incluidos.
- **Panel de asistente IA**: pregunta por tus clústeres en lenguaje natural ("¿qué está fallando
  en shop?", "¿por qué se reinicia este pod?") desde un panel en la parte inferior de cualquier
  página. Consulta con tus propios permisos, solo lee, no guarda nada al cerrarse y cada
  pregunta queda auditada.
- **Servidor MCP para clientes de IA** en `/api/mcp`: maneja Bayplan desde Claude Code, Codex o
  Cursor con un token personal ("¿qué está fallando en shop?", "reinicia el deployment api").
  Cada herramienta pasa por la misma API con tus permisos y se audita como tuya; un token de
  solo lectura solo recibe las herramientas de lectura, y los manifiestos se aplican solo tras
  un dry-run.
- **Topología** por clúster: Ingress → Service → carga de trabajo → pods → volúmenes, en vivo,
  con lo que está roto resaltado y toda la cadena de dependencias de cualquier objeto.
- **Shell web de kubectl** por clúster (solo admin): kubectl real en el servidor con tu
  identidad, cada comando auditado, sin acceso al sistema del servidor.
- **Cuotas de namespace**: ResourceQuota + LimitRange (y el namespace si no existe) desde un
  formulario con dry-run; las pestañas Quotas y LimitRanges muestran el uso frente al límite.
- **Registros de imágenes**: credenciales de registros privados a nivel de plataforma,
  distribuidas como `imagePullSecrets` a los namespaces que elijas en cada clúster; rotar la
  credencial actualiza todas las copias (`/admin/registries` y la pestaña Registries del
  clúster).
- **Consumo real** de CPU y memoria (metrics-server, opcional) por clúster, nodo y pod, junto a
  lo reservado; sin metrics-server el panel lo dice en lugar de pintar ceros.
- **Notificaciones**: reglas sobre los problemas detectados (CrashLoopBackOff, rollouts
  atascados, nodos NotReady, clúster desconectado…) hacia Slack, Teams, webhook o correo
  electrónico, con duración mínima, cooldown y aviso de resolución (`/admin/notifications`).
- **Acciones**: rollout restart y scale de deployments, suplantando la identidad real del
  usuario frente al API server de Kubernetes (atribución nativa en la auditoría de K8s, no solo
  en el `audit_log` interno).
- **Logs y terminal interactivo**: streaming en vivo de los logs de un pod y un terminal
  (equivalente a `kubectl exec -it`) sobre WebSocket, ambos con auditoría de inicio y fin.
- **Navegación**: selectores de clúster y namespace en la barra superior, los recursos del
  clúster en la barra lateral y **búsqueda global con `Ctrl/Cmd+K`** (clústeres, namespaces y
  recursos ya cargados). Los filtros, el orden y la página **viven en la URL**, así que
  cualquier vista es un enlace que se puede compartir y sobrevive a una recarga.
- **Eventos filtrados** (tipo, rango de fechas) y **búsqueda por nombre** en
  pods/namespaces/services/nodos, con tablas ordenables y paginadas.
- **Resumen por clúster**: contadores por estado, una lista priorizada de **"requiere
  atención"** (CrashLoopBackOff, rollouts incompletos, nodos con problemas…), capacidad
  **reservada** (suma de requests frente a allocatable — sin `metrics-server`, esto no es el
  consumo real) y los últimos eventos Warning. Además, una **vista de flota** con los problemas
  de todos los clústeres a la vez. Un clúster inalcanzable siempre se distingue de un clúster
  vacío.
- Métricas de Prometheus en `/metrics`.
- **Auditoría**: `audit_log` de solo inserción (inmutable a nivel de trigger, incluido
  `TRUNCATE`), particionado por mes con retención configurable. Registra las acciones con éxito,
  los **intentos denegados** (`access.denied`, los 403 de autorización y los rechazos de las
  protecciones de seguridad) y, mediante triggers de base de datos, los cambios hechos **por SQL
  directo** en `users`/`role_assignments`/`group_role_mappings` (acciones `db.*`). Se consulta
  desde la UI (`/admin/audit`) con filtros combinables, paginación por cursor y exportación a
  CSV.
- **Perfil y preferencias por usuario** (`/profile`): nombre y correo electrónico, tema
  claro/oscuro/**del sistema**, idioma, densidad de las tablas, filas por página y página de
  inicio. Se guardan en la cuenta, no en el navegador, así que acompañan al usuario en
  cualquier equipo.
- **i18n** y tema claro/oscuro. UI disponible en ocho idiomas: inglés (por defecto), español,
  catalán, alemán, francés, portugués de Brasil, chino simplificado y japonés.

## Stack

- **Backend**: Go, `net/http` (mux nativo de Go 1.22+), `client-go` (informers,
  `remotecommand` para exec), `pgx` (Postgres), `goose` (migraciones embebidas),
  `coder/websocket`, `go-oidc`/`x/oauth2` (SSO OIDC), Helm SDK, `go-git`, `remotedialer`
  (túnel del agente), `prometheus/client_golang`.
- **Frontend**: React 19 + TypeScript + Vite, Tailwind CSS v4, TanStack Query,
  react-i18next, `xterm.js` (terminal), `lucide-react` (iconos). Se compila y se embebe dentro
  del binario de Go (`internal/web`, `embed.FS`) — **un único artefacto** que desplegar.
- **Base de datos**: PostgreSQL.
- **Despliegue**: chart de Helm (recomendado), Docker Compose o Podman Quadlet (ver
  [`deploy/README.md`](deploy/README.md)).

## Estructura del repositorio

```
bayplan/
├── backend/
│   ├── cmd/server/          # punto de entrada del servidor (también `bayplan reencrypt`)
│   ├── cmd/agent/           # agente desplegado dentro de los clústeres gestionados
│   └── internal/
│       ├── ai/              # proveedores de LLM, redacción de datos y prompt de "Explicar"
│       ├── api/             # handlers HTTP y la especificación OpenAPI
│       ├── agent/           # hub de agentes (túnel remotedialer), renderizado de manifiesto y RBAC
│       ├── audit/           # audit_log de solo inserción
│       ├── auth/            # login local, bootstrap, contraseñas
│       ├── backup/          # copias de la configuración de los clústeres y pg_dump del propio Bayplan, a S3
│       ├── cloud/           # descubrimiento de EKS/AKS/GKE y tokens del proveedor
│       ├── config/          # variables de entorno
│       ├── costs/           # coste estimado por namespace
│       ├── crypto/          # keyring (AES-GCM) para todos los secretos almacenados
│       ├── db/              # conexión + migraciones (goose)
│       ├── dockerenv/       # hosts Docker/Podman a través del agente
│       ├── embeddedpg/      # PostgreSQL dentro del contenedor (modo todo en uno)
│       ├── extensions/      # extensiones de UI (herramientas externas en la barra lateral)
│       ├── gitops/          # GitOps ligero (go-git, kustomize, prune)
│       ├── helm/            # Helm SDK sin kubeconfig en disco
│       ├── k8s/             # Manager multiclúster: informers, acciones, exec, logs, métricas
│       ├── kubectl/         # shell web de kubectl (validador + proxy local)
│       ├── mcp/             # servidor MCP para clientes de IA (cliente de la propia API)
│       ├── metrics/         # /metrics (Prometheus)
│       ├── middleware/      # auth, CSRF, rate limit, auditoría de denegaciones
│       ├── notify/          # reglas y canales de notificación
│       ├── oidc/            # SSO OIDC genérico
│       ├── rbac/            # resolución del rol efectivo (global/clúster)
│       ├── registries/      # credenciales de registros de imágenes y pull secrets
│       ├── rotate/          # rotación de la clave de cifrado (`bayplan reencrypt`)
│       ├── scan/            # escaneo de imágenes con Trivy
│       ├── session/         # sesiones en el servidor y tokens de API
│       ├── settings/        # ajustes de la instancia guardados en la base de datos
│       ├── tlsserve/        # HTTPS integrado: ficheros propios, ACME o una CA autofirmada
│       ├── web/             # sirve el frontend embebido
│       └── ws/              # WebSockets: logs, terminal, shell de kubectl
├── frontend/
│   └── src/{pages,components,api,i18n,hooks,lib}/
├── docs/brand/              # kit del logo
└── deploy/
    ├── helm/bayplan/        # chart de Helm
    ├── compose/             # Docker Compose
    ├── airgap/              # lista de imágenes y script de réplica para instalaciones sin conexión
    ├── *.container, *.network, *.volume   # unidades de Podman Quadlet
    ├── rbac/serviceaccount.yaml           # RBAC que aplicar en cada clúster conectado directamente
    ├── rbac/admission-policy.yaml         # sus ValidatingAdmissionPolicies (1.30+)
    └── README.md                          # guía de despliegue en producción
```

## Desarrollo local

Requisitos: Go 1.27+, Node 24 (funciona con 20.19+), Docker (o Podman) para Postgres, y
`kubectl` si vas a conectar un clúster real.

`make dev` y el resto de objetivos del entorno (`scripts/dev.sh`) están escritos para **Linux** y
además necesitan: `docker compose` (o `podman compose`), `jq`, `openssl`, `curl`, `ss`
(iproute2) y `setsid` (util-linux). Usan Docker por defecto; con Podman, ejecútalos como
`CONTAINER_ENGINE=podman make dev`. `make test`, `make vet` y `make lint` no necesitan compilar
el frontend: en el repositorio solo está `backend/internal/web/dist/.gitkeep`, y sin compilar el
binario sirve una página provisional.

### Arranque rápido

Todo pasa por el `Makefile` (`make help` lista los objetivos). Un solo comando levanta el
entorno completo (Postgres + backend + frontend embebido) y crea el admin automáticamente:

```bash
make dev
```

Deja: Postgres en un contenedor, las migraciones aplicadas y la app en `http://localhost:8080`
con el admin ya configurado — **`admin` / `devlocal12345`**. `Ctrl-C` para el backend (Postgres
se queda en marcha; se para con `make stop`).

En el primer arranque `make dev` genera un `.env.dev` local (ignorado por git) con una
`ENCRYPTION_KEY_V1` nueva. **No lo subas ni lo borres nunca**: esa clave determina qué
credenciales de clúster se pueden recuperar. En los arranques siguientes la reutiliza tal cual.

```bash
make dev        # BD + build + backend + admin -> app completa en :8080
make web        # Vite con hot-reload en :5173 (otra terminal, para editar el frontend)
make restart    # recompila y reinicia el backend
make status     # Postgres, backend (/readyz) y clústeres sincronizados
make stop       # para el backend y Postgres
make check      # lo que ejecuta la CI: lint + tests
make reencrypt  # vuelve a cifrar la BD con la clave activa (rotación de claves, ver deploy/README.md)
make reset-db   # recrea la BD desde cero (nuevo bootstrap en el siguiente make dev)
make clean      # purga Postgres (contenedor + volumen) y borra bin/
```

Los objetivos con lógica (esperar al backend, crear el admin, registrar el clúster de pruebas)
llaman a `scripts/dev.sh`.

Para desarrollar el frontend con hot reload: `make dev` en una terminal y `make web` en otra —
y después usa `http://localhost:5173` (Vite redirige `/api` y `/ws` al backend en `:8080`).

#### Registrar un clúster de pruebas automáticamente (opcional)

Para no tener que dar de alta el clúster a mano en `/admin/clusters` cada vez que se recrea la
BD, `make dev` puede registrarlo solo. Copia la plantilla y rellénala (el fichero real está
ignorado por git y nunca se sube):

```bash
cp .cluster.dev.env.example .cluster.dev.env
# edita .cluster.dev.env con el nombre visible, el endpoint y el token, y vuelca la CA:
kubectl config view --raw --minify \
  -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > .cluster.dev.ca.pem
```

En el siguiente `make dev` el clúster se registra tras crear el admin (idempotente: si ya existe
uno con ese endpoint, no hace nada; si el clúster no responde, avisa y sigue). Si dejas la
plantilla sin rellenar, el paso simplemente se omite.

### Arranque manual (equivalente, sin `make dev`)

`make dev` no hace magia: es Postgres en Docker + `.env.dev` + `make build` + arrancar el
binario. Si prefieres los pasos a mano:

```bash
docker run -d --name bayplan-db \
  -e POSTGRES_USER=bayplan -e POSTGRES_PASSWORD=devlocal -e POSTGRES_DB=bayplan \
  -p 5432:5432 postgres:18.6-alpine3.24

cat > .env.dev <<EOF
DATABASE_URL=postgres://bayplan:devlocal@localhost:5432/bayplan?sslmode=disable
LISTEN_ADDR=:8080
LOG_LEVEL=info
COOKIE_SECURE=false
ENCRYPTION_KEY_ACTIVE_ID=v1
ENCRYPTION_KEY_V1=$(openssl rand -base64 32)
EOF

make build
set -a && source .env.dev && set +a && ./bin/bayplan
```

Al arrancar a mano, el admin **no** se crea automáticamente: en los logs verás un **token de
configuración de un solo uso** (caduca a los 30 minutos) — nunca se genera ni se registra en
los logs ninguna contraseña, solo este token. Abre `http://localhost:8080/setup`, pégalo y
elige la contraseña del admin.

### Conectar un clúster real

Desde `/admin/clusters`. Con un **agente** (recomendado, ver `deploy/README.md`) o con una
**credencial cloud** (EKS/AKS/GKE: la credencial del proveedor se guarda en
`/admin/cloud-credentials`, la pestaña "Cloud" descubre los clústeres y el servidor se
autentica con tokens de corta duración del proveedor; el alta devuelve el RBAC que hay que
aplicar para esa identidad). O de forma directa, de tres maneras: pegar un **kubeconfig**
completo (lo que exportan kubeadm, k3s, RKE2, Talos… con `kubectl config view --raw --flatten`;
solo con datos embebidos, un plugin `exec` de aws/gcloud/az no sirve), un **token de
ServiceAccount** + endpoint + CA, o un **certificado de cliente**. En todos los casos antes hay
que aplicar el RBAC del panel en ese clúster; es lo que da permisos de lectura a la SA técnica
y a los grupos suplantados (`bayplan:admins`, `bayplan:editors`, `bayplan:readonly`):

```bash
kubectl apply -f deploy/rbac/serviceaccount.yaml
kubectl apply -f deploy/rbac/admission-policy.yaml   # Kubernetes 1.30+
kubectl create token bayplan-technical -n kube-system --duration=87600h   # si se usa un token
```

El backend valida la conexión contra el clúster real **antes** de guardar nada, y después la
comprueba en vivo cada 30 s: un token caducado o un RBAC que no se volvió a aplicar aparece en
la tabla de clústeres con el error exacto. Las credenciales se **rotan desde "Editar"** sin
borrar el clúster ni perder los permisos concedidos sobre él. Los tipos de recurso secundarios
(ingress, jobs, configmaps…) son informers **opcionales**: si al ClusterRole le falta alguno de
sus permisos, solo avisa esa pestaña; el resto sigue funcionando.

**Compilación y comprobaciones**:

```bash
make build          # frontend + backend, un único binario en bin/bayplan
make test           # go test ./... (backend)
make lint           # gofmt + go vet + lint del frontend (oxlint, claves de locale)
make images         # imágenes del servidor y del agente con podman (VERSION=…)
```

Solo el frontend con hot reload (equivalente a `make web`): `cd frontend && npm run dev`.

## Despliegue en producción

Ver [`deploy/README.md`](deploy/README.md): chart de Helm (`deploy/helm/bayplan`), Docker
Compose, Podman Quadlet, imágenes, RBAC del clúster, SSO (OIDC) desde Ajustes y retención del
`audit_log`.

## CI

`.github/workflows/ci.yml`: lint + test del backend y del frontend, comprobación de
vulnerabilidades, escaneo de imágenes, detección de secretos (gitleaks) y una comprobación de
que `THIRD_PARTY_NOTICES.md` está al día en cada push/PR; en `main` y en los tags `v*` publica
las imágenes en GHCR, y en los tags `v*` también el chart y la release de GitHub. No despliega.

## Licencia

Bayplan es software libre bajo la [licencia Apache 2.0](LICENSE). Puedes usarlo, modificarlo y
redistribuirlo, también con fines comerciales; a cambio, los ficheros que modifiques deben
indicar que han sido cambiados, y hay que conservar el fichero [`NOTICE`](NOTICE). El nombre y
el logo de Bayplan no forman parte de la licencia: un fork modificado necesita su propio nombre
([`TRADEMARKS.md`](TRADEMARKS.md)). Las licencias del software de terceros incluido en los
binarios y las imágenes están en [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

Para contribuir, ver [`CONTRIBUTING.md`](CONTRIBUTING.md) (DCO, sin CLA); para informar de una
vulnerabilidad, [`SECURITY.md`](SECURITY.md).
