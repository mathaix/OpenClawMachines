#!/usr/bin/env bash
# Local development helper for the trusted AUTH_MODE=dev control-plane path.
#
# This script is for local UI/control-plane smoke testing. It does not configure
# Cloudflare, Firebase, Firecracker, or a KVM worker host.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${OCM_LOCAL_DEV_ENV_FILE:-${ROOT_DIR}/.env.local}"
FRONTEND_ENV_FILE="${OCM_LOCAL_DEV_FRONTEND_ENV_FILE:-${ROOT_DIR}/frontend/.env.local}"

POSTGRES_CONTAINER="${OCM_LOCAL_POSTGRES_CONTAINER:-ocm-postgres}"
POSTGRES_IMAGE="${OCM_LOCAL_POSTGRES_IMAGE:-postgres:16}"
POSTGRES_USER="${OCM_LOCAL_POSTGRES_USER:-ocm}"
POSTGRES_PASSWORD="${OCM_LOCAL_POSTGRES_PASSWORD:-ocm}"
POSTGRES_DB="${OCM_LOCAL_POSTGRES_DB:-ocm}"
POSTGRES_PORT="${OCM_LOCAL_POSTGRES_PORT:-5432}"

usage() {
	cat <<'EOF'
Usage: scripts/local-dev.sh <command>

Commands:
  env        Create .env.local and frontend/.env.local if they do not exist
  postgres   Start a local Docker Postgres container
  migrate    Run backend SQL migrations against DATABASE_URL
  backend    Start Postgres if local, run migrations, then start backend :8080
  frontend   Install frontend deps if needed, then start Vite :5173
  status     Check backend/frontend health and current dev auth user
  stop       Stop the local Docker Postgres container
  help       Show this help

Typical use:
  Terminal 1: scripts/local-dev.sh postgres
  Terminal 2: scripts/local-dev.sh backend
  Terminal 3: scripts/local-dev.sh frontend

Then open http://localhost:5173/dashboard.

Set OCM_LOCAL_DEV_SKIP_POSTGRES=1 to use your own DATABASE_URL instead of the
Docker Postgres helper.
EOF
}

random_hex() {
	local bytes="$1"
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex "$bytes"
		return
	fi
	if [ -r /dev/urandom ] && command -v od >/dev/null 2>&1; then
		od -An -N"$bytes" -tx1 /dev/urandom | tr -d ' \n'
		return
	fi
	printf '%0*s' "$((bytes * 2))" 0
}

ensure_backend_env() {
	if [ -f "$ENV_FILE" ]; then
		printf 'Using existing %s\n' "$ENV_FILE"
		return
	fi

	mkdir -p "$(dirname "$ENV_FILE")"
	cat >"$ENV_FILE" <<EOF
CONTROL_PLANE_PROFILE=local
DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable
AUTH_MODE=dev
OCM_ALLOW_DEV_AUTH=1
DEV_USER_EMAIL=dev@localhost
JWT_SECRET=local-dev-$(random_hex 24)
SECRET_ENCRYPTION_KEY=$(random_hex 16)
FC_AGENT_TOKEN=local-agent-$(random_hex 16)
BACKEND_URL=http://localhost:8080
FRONTEND_URL=http://localhost:5173
CORS_ORIGINS=http://localhost:5173
PORT=8080
EOF
	printf 'Created %s\n' "$ENV_FILE"
}

ensure_frontend_env() {
	if [ -f "$FRONTEND_ENV_FILE" ]; then
		printf 'Using existing %s\n' "$FRONTEND_ENV_FILE"
		return
	fi

	mkdir -p "$(dirname "$FRONTEND_ENV_FILE")"
	cat >"$FRONTEND_ENV_FILE" <<'EOF'
VITE_API_URL=/api
VITE_DATA_PLANE_DOMAIN=localhost
VITE_COOKIE_DOMAIN=
VITE_OCM_ADMIN_EMAILS=dev@localhost
EOF
	printf 'Created %s\n' "$FRONTEND_ENV_FILE"
}

load_backend_env() {
	local existing_database_url="${DATABASE_URL:-}"
	ensure_backend_env
	set -a
	# shellcheck disable=SC1090
	. "$ENV_FILE"
	set +a
	if [ "${OCM_LOCAL_DEV_SKIP_POSTGRES:-}" = "1" ] && [ -n "$existing_database_url" ]; then
		export DATABASE_URL="$existing_database_url"
	fi
}

docker_cmd() {
	if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
		DOCKER=(docker)
		return
	fi
	if command -v sudo >/dev/null 2>&1 && sudo -n docker info >/dev/null 2>&1; then
		DOCKER=(sudo -n docker)
		return
	fi
	echo "ERROR: Docker is not available. Install Docker, start it, or set OCM_LOCAL_DEV_SKIP_POSTGRES=1 with your own DATABASE_URL." >&2
	exit 1
}

container_exists() {
	"${DOCKER[@]}" ps -a --format '{{.Names}}' | grep -Fxq "$POSTGRES_CONTAINER"
}

container_running() {
	"${DOCKER[@]}" ps --format '{{.Names}}' | grep -Fxq "$POSTGRES_CONTAINER"
}

start_postgres() {
	docker_cmd

	if container_running; then
		printf 'Postgres container already running: %s\n' "$POSTGRES_CONTAINER"
	elif container_exists; then
		"${DOCKER[@]}" start "$POSTGRES_CONTAINER" >/dev/null
		printf 'Started existing Postgres container: %s\n' "$POSTGRES_CONTAINER"
	else
		"${DOCKER[@]}" run -d \
			--name "$POSTGRES_CONTAINER" \
			-e POSTGRES_USER="$POSTGRES_USER" \
			-e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
			-e POSTGRES_DB="$POSTGRES_DB" \
			-p "${POSTGRES_PORT}:5432" \
			"$POSTGRES_IMAGE" >/dev/null
		printf 'Started new Postgres container: %s\n' "$POSTGRES_CONTAINER"
	fi

	printf 'Waiting for Postgres readiness'
	for _ in $(seq 1 60); do
		if "${DOCKER[@]}" exec "$POSTGRES_CONTAINER" pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
			printf '\nPostgres ready on localhost:%s\n' "$POSTGRES_PORT"
			return
		fi
		printf '.'
		sleep 1
	done
	printf '\nERROR: Postgres did not become ready in time.\n' >&2
	exit 1
}

should_manage_postgres() {
	if [ "${OCM_LOCAL_DEV_SKIP_POSTGRES:-}" = "1" ]; then
		return 1
	fi
	case "${DATABASE_URL:-}" in
	*localhost:* | *127.0.0.1:*) return 0 ;;
	*) return 1 ;;
	esac
}

run_migrations() {
	load_backend_env
	if command -v psql >/dev/null 2>&1; then
		(cd "$ROOT_DIR" && scripts/run-migrations.sh)
		return
	fi

	docker_cmd
	printf 'psql not found on host; running migrations through %s container\n' "$POSTGRES_IMAGE"
	"${DOCKER[@]}" run --rm \
		--network host \
		-v "${ROOT_DIR}:/repo:ro" \
		-w /repo \
		-e DATABASE_URL="$DATABASE_URL" \
		"$POSTGRES_IMAGE" \
		bash scripts/run-migrations.sh
}

start_backend() {
	load_backend_env
	if should_manage_postgres; then
		start_postgres
	fi
	run_migrations
	(cd "$ROOT_DIR/backend" && exec go run ./cmd/server/)
}

start_frontend() {
	ensure_frontend_env
	cd "$ROOT_DIR/frontend"
	if [ ! -d node_modules ]; then
		npm ci
	fi
	exec npm run dev
}

status() {
	load_backend_env
	(cd "$ROOT_DIR" && make status)
	printf '\nAuth user:\n'
	curl -sS "http://localhost:${PORT:-8080}/api/auth/me" || true
	printf '\n'
}

stop_postgres() {
	docker_cmd
	if container_running; then
		"${DOCKER[@]}" stop "$POSTGRES_CONTAINER" >/dev/null
		printf 'Stopped Postgres container: %s\n' "$POSTGRES_CONTAINER"
	else
		printf 'Postgres container is not running: %s\n' "$POSTGRES_CONTAINER"
	fi
}

cmd="${1:-help}"
case "$cmd" in
env)
	ensure_backend_env
	ensure_frontend_env
	;;
postgres)
	ensure_backend_env
	start_postgres
	;;
migrate)
	run_migrations
	;;
backend)
	start_backend
	;;
frontend)
	start_frontend
	;;
status)
	status
	;;
stop)
	stop_postgres
	;;
help | --help | -h)
	usage
	;;
*)
	usage >&2
	exit 1
	;;
esac
