#!/usr/bin/env bash
# Apply or revert db/migrations.
#   scripts/migrate.sh [up]   apply every pending NNNN_name.up.sql in filename order
#   scripts/migrate.sh down   revert the latest applied version with its .down.sql
# Each migration runs in one transaction together with its schema_migrations row.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
MIG_DIR="$ROOT/db/migrations"
CMD="${1:-up}"

# psql_run [psql args...]: SQL on stdin. Uses the local psql + DATABASE_URL when
# available, otherwise the db service from docker compose.
psql_run() {
  if command -v psql >/dev/null 2>&1 && [ -n "${DATABASE_URL:-}" ]; then
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -X -q "$@"
  else
    docker compose exec -T db psql -U alphahack -d alphahack -v ON_ERROR_STOP=1 -X -q "$@"
  fi
}

ensure_table() {
  printf '%s\n' "SET client_min_messages TO warning;" "CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz DEFAULT now());" | psql_run
}

sql_quote() {
  local s="$1"
  printf "'%s'" "${s//\'/\'\'}"
}

case "$CMD" in
  up)
    ensure_table
    APPLIED="$(psql_run -tA -c "SELECT version FROM schema_migrations ORDER BY version")"
    for f in "$MIG_DIR"/*.up.sql; do
      [ -e "$f" ] || continue
      base="$(basename "$f" .up.sql)"
      if grep -Fxq "$base" <<<"$APPLIED"; then
        echo "skip   $base (already applied)"
        continue
      fi
      echo "apply  $base"
      {
        echo "BEGIN;"
        cat "$f"
        printf '\nINSERT INTO schema_migrations (version) VALUES (%s);\nCOMMIT;\n' "$(sql_quote "$base")"
      } | psql_run
    done
    echo "migrate up: done"
    ;;
  down)
    ensure_table
    LATEST="$(psql_run -tA -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")"
    if [ -z "$LATEST" ]; then
      echo "migrate down: nothing applied, nothing to revert"
      exit 0
    fi
    DOWN="$MIG_DIR/$LATEST.down.sql"
    if [ ! -f "$DOWN" ]; then
      echo "migrate down: missing $DOWN" >&2
      exit 1
    fi
    echo "revert $LATEST"
    {
      echo "BEGIN;"
      cat "$DOWN"
      printf '\nDELETE FROM schema_migrations WHERE version = %s;\nCOMMIT;\n' "$(sql_quote "$LATEST")"
    } | psql_run
    echo "migrate down: done"
    ;;
  *)
    echo "usage: $0 [up|down]" >&2
    exit 2
    ;;
esac
