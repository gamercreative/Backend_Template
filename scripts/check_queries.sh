#!/usr/bin/env bash
# Validates SQL files against the live schema and enforces the layering rules
# from AGENTS.md. Prints PASS/FAIL per check; exits 1 if any check fails.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
SQL_DIR="$ROOT/internal/queries/sql"

FAILS=0

pass() { echo "PASS  $1"; }
fail() {
  echo "FAIL  $1"
  FAILS=$((FAILS + 1))
}
detail() { printf '      %s\n' "$1"; }

# psql_run [psql args...]: SQL on stdin, same routing as migrate.sh.
psql_run() {
  if command -v psql >/dev/null 2>&1 && [ -n "${DATABASE_URL:-}" ]; then
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -X -q "$@"
  else
    docker compose exec -T db psql -U alphahack -d alphahack -v ON_ERROR_STOP=1 -X -q "$@"
  fi
}

# ---------------------------------------------------------------- check 1
check_sql_files() {
  local name="every internal/queries/sql/*.sql prepares against the schema"
  local f base body err bad=0 count=0
  for f in "$SQL_DIR"/*.sql; do
    [ -e "$f" ] || continue
    count=$((count + 1))
    body="$(cat "$f")"
    # strip trailing whitespace and semicolons
    while :; do
      case "$body" in
        *[[:space:]]) body="${body%?}" ;;
        *\;) body="${body%?}" ;;
        *) break ;;
      esac
    done
    if err="$(printf 'PREPARE q AS %s;\nDEALLOCATE q;\n' "$body" | psql_run 2>&1 >/dev/null)"; then
      :
    else
      bad=$((bad + 1))
      detail "${f#$ROOT/}"
      printf '%s\n' "$err" | sed -e 's/^/        /'
    fi
  done
  if [ "$count" -eq 0 ]; then
    fail "$name (no .sql files found in ${SQL_DIR#$ROOT/})"
  elif [ "$bad" -gt 0 ]; then
    fail "$name ($bad of $count failing)"
  else
    pass "$name ($count files)"
  fi
}

# ---------------------------------------------------------------- check 2
# list_importers PATTERN: relative paths of .go files whose import lines match
# PATTERN (ERE, matched between the import quotes).
list_importers() {
  local pat="$1" f
  while IFS= read -r f; do
    if grep -E -q "^[[:space:]]*(import[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*[[:space:]]+)?\"($pat)\"" "$f"; then
      echo "${f#$ROOT/}"
    fi
  done < <(find "$ROOT" -name '*.go' -type f -not -path '*/.git/*')
  return 0
}

# check_import LABEL PATTERN SCOPE [ALLOWED_PREFIX...]
# Fails for every importer under SCOPE (empty = whole repo) that is not under
# one of the ALLOWED_PREFIX paths.
check_import() {
  local label="$1" pat="$2" scope="$3" rel p allowed bad=""
  shift 3
  for rel in $(list_importers "$pat"); do
    case "$rel" in "$scope"*) ;; *) continue ;; esac
    allowed=0
    for p in ${1+"$@"}; do
      case "$rel" in "$p"*) allowed=1 ;; esac
    done
    if [ "$allowed" -eq 0 ]; then
      bad="$bad $rel"
    fi
  done
  if [ -n "$bad" ]; then
    fail "$label"
    for rel in $bad; do detail "imports: $rel"; done
  else
    pass "$label"
  fi
}

check_layering() {
  check_import "jackc/pgx only under internal/queries and cmd/server" \
    'github\.com/jackc/pgx[^"]*' "" "internal/queries/" "cmd/server/"
  check_import "anthropic-sdk-go only under internal/ai" \
    '[^"]*anthropic-sdk-go[^"]*' "" "internal/ai/"
  check_import "internal/services does not import net/http" \
    'net/http' "internal/services/"
  check_import "internal/services does not import alphahack/internal/http" \
    'alphahack/internal/http' "internal/services/"
  check_import "internal/handlers does not import alphahack/internal/ai" \
    'alphahack/internal/ai' "internal/handlers/"
}

# ---------------------------------------------------------------- check 3
check_raw_sql() {
  local name="no raw SQL in non-test .go files outside internal/queries"
  local pat='INSERT INTO|DELETE FROM|SELECT .* FROM|UPDATE [a-z_]+ SET'
  local hits
  hits="$(find "$ROOT" -name '*.go' -type f -not -name '*_test.go' -not -path '*/.git/*' \
    -not -path "$ROOT/internal/queries/*" -print0 \
    | xargs -0 grep -nE "$pat" /dev/null || true)"
  if [ -n "$hits" ]; then
    fail "$name"
    printf '%s\n' "$hits" | while IFS= read -r line; do detail "${line#$ROOT/}"; done
  else
    pass "$name"
  fi
}

check_sql_files
check_layering
check_raw_sql

echo
if [ "$FAILS" -gt 0 ]; then
  echo "check-queries: $FAILS check(s) failed"
  exit 1
fi
echo "check-queries: all checks passed"
