#!/usr/bin/env bash
# Local development environment (Go backend + React frontend), called by the
# Makefile: `make dev`, `make web`, `make stop`… (`make help` lists them). Only
# Postgres runs in a container; the backend and Vite run on the host. The logic that
# does not fit in a Makefile recipe lives here: waiting for /readyz, creating
# the dev admin from the setup token, registering the test cluster.
set -euo pipefail
cd "$(dirname "$0")/.."

SERVICE_NAME="bayplan"
COMPOSE_FILE="docker-compose.dev.yml"
ENV_FILE=".env.dev"
LOG_FILE=".bayplan-dev.log"          # gitignored via *.log
BASE_URL="http://localhost:8080"
DEV_ADMIN_PASSWORD="devlocal12345"   # >=12 chars (MinPasswordLength in auth/password.go)
CLUSTER_ENV_FILE=".cluster.dev.env"  # gitignored; dev cluster to register on startup
# docker (default) or podman: both provide `<engine> compose` (podman 4.7+
# delegates it to docker-compose or podman-compose).
CONTAINER_ENGINE="${CONTAINER_ENGINE:-docker}"

die() {
  echo "❌ $1"
  exit 1
}

check_dependencies() {
  command -v "$CONTAINER_ENGINE" >/dev/null 2>&1 || die "$CONTAINER_ENGINE is not installed or not in the PATH (CONTAINER_ENGINE=docker|podman)."
  command -v go >/dev/null 2>&1      || die "Go is not installed or not in the PATH."
  command -v npm >/dev/null 2>&1     || die "npm (Node) is not installed or not in the PATH."
  command -v openssl >/dev/null 2>&1 || die "openssl is not installed or not in the PATH."
  command -v curl >/dev/null 2>&1    || die "curl is not installed or not in the PATH."
  command -v jq >/dev/null 2>&1      || die "jq is not installed or not in the PATH."
  # Linux tools: ss finds the backend by its port, setsid detaches it.
  command -v ss >/dev/null 2>&1      || die "ss (iproute2) is not installed or not in the PATH."
  command -v setsid >/dev/null 2>&1  || die "setsid (util-linux) is not installed or not in the PATH."
}

execute_compose() {
  "$CONTAINER_ENGINE" compose -f "$COMPOSE_FILE" -p "$SERVICE_NAME" "$@"
}

# Installs the frontend dependencies when missing or outdated (the Makefile
# target compares them with package-lock.json).
ensure_frontend_deps() {
  make --no-print-directory frontend/node_modules
}

do_build() {
  echo "⚙️ Building frontend + backend..."
  make --no-print-directory build
}

# Generates .env.dev only if it does not exist: ENCRYPTION_KEY_V1 must stay stable, or the
# already stored cluster credentials can no longer be decrypted. Never overwrites it.
ensure_env() {
  if [[ -f "$ENV_FILE" ]]; then
    echo "✅ $ENV_FILE already exists, reusing it."
    return
  fi
  echo "⚙️ Generating $ENV_FILE (new encryption key)..."
  cat > "$ENV_FILE" <<EOF
DATABASE_URL=postgres://bayplan:devlocal@localhost:5432/bayplan?sslmode=disable
LISTEN_ADDR=:8080
LOG_LEVEL=info
COOKIE_SECURE=false
ENCRYPTION_KEY_ACTIVE_ID=v1
ENCRYPTION_KEY_V1=$(openssl rand -base64 32)
EOF
}

# Waits until the backend answers 200 on /readyz.
wait_ready() {
  echo "⏳ Waiting for the backend to be ready..."
  for _ in $(seq 1 60); do
    if curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "The backend did not answer on /readyz. Check $LOG_FILE."
}

# Creates the first admin if still pending: extracts the one-time token from the log
# (msg of logger.Warn(token), base64url of 32 bytes = 43 chars) and redeems it on /setup.
auto_setup() {
  if ! curl -fsS "$BASE_URL/api/auth/status" | grep -q '"setupPending":true'; then
    echo "✅ The admin already exists, no setup needed."
    return
  fi
  local token
  token=$(grep -oE '"msg":"[A-Za-z0-9_-]{43}"' "$LOG_FILE" | head -1 | grep -oE '[A-Za-z0-9_-]{43}' || true)
  [[ -n "$token" ]] || die "Could not extract the setup token from $LOG_FILE."
  echo "🔑 Creating admin with the setup token..."
  curl -fsS -X POST "$BASE_URL/api/auth/setup" \
    -H 'Content-Type: application/json' \
    -d "{\"token\":\"$token\",\"newPassword\":\"$DEV_ADMIN_PASSWORD\"}" >/dev/null \
    || die "POST /api/auth/setup failed."
}

# Registers in Bayplan the dev cluster described in .cluster.dev.env, so it does not have
# to be added by hand in /admin/clusters every time the DB is recreated. Idempotent: if it
# is already there, does nothing. Does not abort the startup if the cluster does not answer
# (only warns): the Bayplan environment is still useful with the test cluster switched off.
auto_register_cluster() {
  [[ -f "$CLUSTER_ENV_FILE" ]] || return 0
  # shellcheck source=/dev/null
  source "$CLUSTER_ENV_FILE"
  local ca="${CLUSTER_CA_FILE:-.cluster.dev.ca.pem}"
  if [[ "${CLUSTER_SA_TOKEN:-}" == "PASTE_TOKEN_HERE" || -z "${CLUSTER_SA_TOKEN:-}" ]] \
     || [[ ! -f "$ca" ]] || ! grep -q 'BEGIN CERTIFICATE' "$ca" 2>/dev/null; then
    echo "ℹ️  $CLUSTER_ENV_FILE not filled in (token/CA) — skipping the cluster registration."
    return 0
  fi

  local jar; jar=$(mktemp)
  trap 'rm -f "$jar"' RETURN
  if ! curl -fsS -c "$jar" -X POST "$BASE_URL/api/auth/login" \
       -H 'Content-Type: application/json' \
       -d "{\"username\":\"admin\",\"password\":\"$DEV_ADMIN_PASSWORD\"}" >/dev/null; then
    echo "⚠️  Could not log in as admin to register the cluster."
    return 0
  fi

  # Idempotency by endpoint, not by name: the display name can be changed from the
  # panel and we must not end up with the same cluster registered twice.
  if curl -fsS -b "$jar" "$BASE_URL/api/clusters" | grep -q "\"apiEndpoint\":\"$CLUSTER_API_ENDPOINT\""; then
    echo "✅ Cluster $CLUSTER_API_ENDPOINT already registered."
    return 0
  fi

  local csrf; csrf=$(grep -oE 'bayplan_csrf[[:space:]]+[^[:space:]]+$' "$jar" | awk '{print $NF}')
  local payload
  payload=$(jq -n \
    --arg dn "$CLUSTER_DISPLAY_NAME" \
    --arg ep "$CLUSTER_API_ENDPOINT" \
    --rawfile ca "$ca" \
    --arg tok "$CLUSTER_SA_TOKEN" \
    '{displayName:$dn, apiEndpoint:$ep, caCert:$ca, saToken:$tok}')

  echo "🔗 Registering cluster '$CLUSTER_DISPLAY_NAME' ($CLUSTER_API_ENDPOINT)..."
  if curl -fsS -b "$jar" -X POST "$BASE_URL/api/clusters" \
       -H 'Content-Type: application/json' \
       -H "X-CSRF-Token: $csrf" \
       -d "$payload" >/dev/null; then
    echo "✅ Cluster registered."
  else
    echo "⚠️  Could not register the cluster (switched off or expired credentials?). The rest of the environment is still up."
  fi
}

# PID of the backend listening on :8080, if any. Looked up by port and not
# by process name: 'restart' must be able to kill a backend started from
# another terminal (or orphaned from a previous session).
backend_pid() {
  # '|| true': with pipefail, a grep with no match would bring down the whole script.
  ss -ltnp 2>/dev/null | awk '/:8080 /{print $NF}' | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2 || true
}

stop_backend() {
  local pid; pid=$(backend_pid)
  if [[ -n "$pid" ]]; then
    echo "🛑 Stopping backend (pid $pid)..."
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 20); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.5
    done
    kill -9 "$pid" 2>/dev/null || true
  fi
}

# Starts the already-built binary, detached from this terminal (nohup + setsid):
# it survives the session that launched it being closed. The log is rotated on
# start so auto_setup ALWAYS finds the token of the current start, not an old one.
launch_backend() {
  ensure_env
  [[ -x bin/bayplan ]] || die "bin/bayplan does not exist: run make build first."
  echo "🚀 Starting backend..."
  [[ -f "$LOG_FILE" ]] && mv -f "$LOG_FILE" "$LOG_FILE.1"
  set -a
  # shellcheck source=/dev/null
  source "$ENV_FILE"
  set +a
  setsid nohup ./bin/bayplan > "$LOG_FILE" 2>&1 < /dev/null &
  disown || true
}

start_backend() {
  do_build
  launch_backend
}

[[ $# -eq 1 ]] || die "One argument is required. Received: $#."

if [[ -z "${CI:-}" ]]; then
  check_dependencies
fi

case "$1" in
  dev)
    echo "🚀 Bringing up the development environment..."
    execute_compose up -d db
    stop_backend
    start_backend
    wait_ready
    auto_setup
    auto_register_cluster
    echo "═══════════════════════════════════════════════════════════════"
    echo "✅ Environment ready:  $BASE_URL"
    echo "   Login:  admin / $DEV_ADMIN_PASSWORD"
    echo "   Frontend with hot-reload:  make web  (in another terminal)"
    echo "   Backend in the background: make restart to reload it, make stop to stop it."
    echo "═══════════════════════════════════════════════════════════════"
    tail -f "$LOG_FILE"
    ;;
  restart)
    stop_backend
    start_backend
    wait_ready
    auto_setup
    echo "✅ Backend restarted: $BASE_URL (log in $LOG_FILE)"
    ;;
  backend)
    stop_backend
    launch_backend
    wait_ready
    auto_setup
    echo "✅ Backend started: $BASE_URL (log in $LOG_FILE)"
    ;;
  reencrypt)
    # Re-encrypts the DB with the active key. To try out a rotation locally:
    #   ENCRYPTION_KEY_ACTIVE_ID=v2 ENCRYPTION_KEY_V2=<base64 32 bytes> make reencrypt
    # Variables already exported by the caller win over those in the env
    # file (otherwise there would be no way to change the active key).
    ensure_env
    [[ -x bin/bayplan ]] || die "bin/bayplan does not exist: run make build first."
    KEEP_ACTIVE="${ENCRYPTION_KEY_ACTIVE_ID:-}"
    set -a
    # shellcheck source=/dev/null
    source "$ENV_FILE"
    set +a
    [[ -n "$KEEP_ACTIVE" ]] && export ENCRYPTION_KEY_ACTIVE_ID="$KEEP_ACTIVE"
    ./bin/bayplan reencrypt
    ;;
  status)
    execute_compose ps
    if curl -fsS -m 2 "$BASE_URL/readyz" 2>/dev/null | jq . ; then
      echo "✅ Backend up (pid $(backend_pid))"
    else
      echo "❌ Backend not responding on $BASE_URL"
    fi
    ;;
  web)
    ensure_frontend_deps
    echo "🚀 Starting Vite (hot-reload)..."
    cd frontend && npm run dev
    ;;
  db)
    echo "🚀 Starting only Postgres..."
    execute_compose up -d db
    ;;
  stop)
    stop_backend
    echo "🛑 Stopping Postgres..."
    execute_compose stop
    ;;
  clean)
    echo "🗑️ Purging Postgres (container + volume) and binaries..."
    execute_compose down --rmi local --volumes
    rm -rf bin
    ;;
  reset-db)
    echo "🧹 Recreating Postgres from scratch..."
    execute_compose stop db
    execute_compose rm -f db
    execute_compose up -d --force-recreate db
    ;;
  *)
    die "Invalid command: $1 (see make help)"
    ;;
esac

exit 0
