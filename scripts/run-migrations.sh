#!/bin/bash
# Run database migrations against Neon Postgres
#
# Usage:
#   scripts/run-migrations.sh                    # uses DATABASE_URL env var
#   scripts/run-migrations.sh --from-secret      # fetches from GCP Secret Manager
#
# Migrations are applied in order. A schema_migrations table tracks which
# ones have run. Each migration runs inside a transaction so partial
# failures don't leave the schema in a broken state. Migrations that must run
# outside a transaction, such as CREATE INDEX CONCURRENTLY, can opt out with:
#   -- migrate: no-transaction

set -euo pipefail

MIGRATIONS_DIR="backend/migrations"

# --- Resolve DATABASE_URL ---
if [ "${1:-}" = "--from-secret" ]; then
    DATABASE_URL=$(gcloud secrets versions access latest \
        --secret=OCM_DATABASE_URL --project="${GCP_PROJECT:-clarateach}" 2>/dev/null) \
        || { echo "ERROR: Could not fetch DATABASE_URL from Secret Manager"; exit 1; }
elif [ -z "${DATABASE_URL:-}" ]; then
    echo "ERROR: DATABASE_URL not set. Use --from-secret or set DATABASE_URL env var."
    exit 1
fi

# --- Ensure psql is available ---
if ! command -v psql >/dev/null 2>&1; then
    echo "ERROR: psql not found. Install postgresql-client."
    exit 1
fi

# --- Create tracking table if needed ---
psql "$DATABASE_URL" -q <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INT PRIMARY KEY,
    filename    TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
SQL

# --- Get last applied version ---
LAST_VERSION=$(psql "$DATABASE_URL" -tA -c \
    "SELECT COALESCE(MAX(version), 0) FROM schema_migrations;" 2>/dev/null)
echo "Current schema version: ${LAST_VERSION}"

# --- Apply pending migrations ---
APPLIED=0
for file in "$MIGRATIONS_DIR"/*.sql; do
    [ -f "$file" ] || continue

    BASENAME=$(basename "$file")
    # Extract version number: 034_workflow_runs.sql -> 34
    VERSION=$(echo "$BASENAME" | grep -oE '^[0-9]+' | sed 's/^0*//')
    [ -z "$VERSION" ] && continue

    if [ "$VERSION" -le "$LAST_VERSION" ]; then
        continue
    fi

    echo "Applying: ${BASENAME} (v${VERSION})..."

    if grep -qiE '^--[[:space:]]*migrate:[[:space:]]*no-transaction' "$file"; then
        if ! psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$file"; then
            echo "FAILED: ${BASENAME}"
            exit 1
        fi
        if ! psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c \
            "INSERT INTO schema_migrations (version, filename) VALUES (${VERSION}, '${BASENAME}');"; then
            echo "FAILED: ${BASENAME} (record schema version)"
            exit 1
        fi
    # Run migration in a transaction with the tracking insert.
    elif ! psql "$DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction <<EOSQL
$(cat "$file")

INSERT INTO schema_migrations (version, filename) VALUES (${VERSION}, '${BASENAME}');
EOSQL
    then
        echo "FAILED: ${BASENAME}"
        exit 1
    fi

    echo "  OK: ${BASENAME}"
    APPLIED=$((APPLIED + 1))
done

if [ "$APPLIED" -eq 0 ]; then
    echo "No pending migrations."
else
    echo "Applied ${APPLIED} migration(s). New version: $(psql "$DATABASE_URL" -tA -c \
        "SELECT MAX(version) FROM schema_migrations;")"
fi
