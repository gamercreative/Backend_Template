#!/usr/bin/env bash
# Smoke test: curls every endpoint of a running server and checks status codes
# and the error format. Usage: BASE_URL=http://localhost:8080 scripts/smoke.sh
set -uo pipefail

BASE="${BASE_URL:-http://localhost:8080}"
PASS=0
FAIL=0
BODY=""

# req METHOD PATH EXPECTED_STATUS [JSON_BODY] [EXPECTED_ERROR_CODE]
req() {
  local method="$1" path="$2" want="$3" data="${4:-}" code="${5:-}"
  local out status
  if [ -n "$data" ]; then
    out=$(curl -sS -m 600 -X "$method" "$BASE$path" -H 'Content-Type: application/json' -d "$data" -w $'\n%{http_code}')
  else
    out=$(curl -sS -m 600 -X "$method" "$BASE$path" -w $'\n%{http_code}')
  fi
  status="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
  local ok=1
  [ "$status" = "$want" ] || ok=0
  if [ -n "$code" ]; then
    case "$BODY" in
      *"\"error\":{\"code\":\"$code\""*"\"request_id\":\""*) ;;
      *) ok=0 ;;
    esac
  fi
  if [ "$ok" = 1 ]; then
    PASS=$((PASS + 1))
    printf 'PASS %-6s %-45s %s\n' "$method" "$path" "$status"
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL %-6s %-45s got %s want %s %s\n     %s\n' "$method" "$path" "$status" "$want" "$code" "$BODY"
  fi
}

# stream PATH JSON_BODY: POST and expect a 200 event stream holding start and done events.
stream() {
  local path="$1" data="$2" out status
  out=$(curl -sS -N -m 600 -X POST "$BASE$path" -H 'Content-Type: application/json' -d "$data" -w $'\n%{http_code}')
  status="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
  case "$status|$BODY" in
    200\|*"event: start"*"event: delta"*"event: done"*)
      PASS=$((PASS + 1))
      printf 'PASS %-6s %-45s %s (start, delta, done)\n' POST "$path" "$status" ;;
    *)
      FAIL=$((FAIL + 1))
      printf 'FAIL %-6s %-45s got %s, want an event stream with start, delta and done\n     %s\n' POST "$path" "$status" "$BODY" ;;
  esac
}

# first_id: the first "id" value in $BODY. id_after KEY: the id of the object under KEY.
first_id() { printf '%s' "$BODY" | sed -n 's/^[^"]*"id":"\([^"]*\)".*/\1/p' | head -n 1; }
id_after() { printf '%s' "$BODY" | sed -n "s/.*\"$1\":{\"id\":\"\([^\"]*\)\".*/\1/p" | head -n 1; }

echo "== smoke test against $BASE"

req GET /healthz 200

# agents
req POST /v1/agents 400 '{}' validation_failed
req POST /v1/agents 400 '{"name":"x","bogus":1}' bad_request
req POST /v1/agents 400 '{not json' bad_request
req POST /v1/agents 201 '{"name":"Smoke Agent","system_prompt":"You are a terse test agent."}'
AGENT_ID=$(first_id)
req GET /v1/agents 200
req GET "/v1/agents?limit=abc" 400 '' bad_request
req GET "/v1/agents/$AGENT_ID" 200
req PATCH "/v1/agents/$AGENT_ID" 200 '{"name":"Smoke Agent v2"}'
req PATCH "/v1/agents/$AGENT_ID" 400 '{}' validation_failed
req GET /v1/agents/does-not-exist 404 '' not_found
req PATCH /v1/agents/does-not-exist 404 '{"name":"x"}' not_found
req DELETE /v1/agents/does-not-exist 404 '' not_found

# conversations
req POST /v1/conversations 400 '{}' validation_failed
req POST /v1/conversations 404 '{"agent_id":"does-not-exist"}' not_found
req POST /v1/conversations 201 "{\"agent_id\":\"$AGENT_ID\",\"title\":\"smoke\"}"
CONV_ID=$(first_id)
req GET /v1/conversations 200
req GET "/v1/conversations?agent_id=$AGENT_ID" 200
req GET "/v1/conversations/$CONV_ID" 200
req GET /v1/conversations/does-not-exist 404 '' not_found

# tools + chat
req GET /v1/tools 200
req POST "/v1/conversations/$CONV_ID/messages" 400 '{"content":"   "}' validation_failed
req POST /v1/conversations/does-not-exist/messages 404 '{"content":"hi"}' not_found
req POST "/v1/conversations/$CONV_ID/messages" 201 '{"content":"Say hello in three words."}'
RUN_ID=$(id_after run)
req POST "/v1/conversations/$CONV_ID/messages" 201 '{"content":"/tool get_current_time {}"}'
stream "/v1/conversations/$CONV_ID/messages/stream" '{"content":"Count from one to three."}'
req POST "/v1/conversations/$CONV_ID/messages/stream" 400 '{"content":""}' validation_failed
req POST /v1/conversations/does-not-exist/messages/stream 404 '{"content":"hi"}' not_found
req GET "/v1/conversations/$CONV_ID/messages" 200
req GET /v1/conversations/does-not-exist/messages 404 '' not_found
req GET "/v1/conversations/$CONV_ID/runs" 200
req GET /v1/conversations/does-not-exist/runs 404 '' not_found
req GET "/v1/runs/$RUN_ID" 200
req GET /v1/runs/does-not-exist 404 '' not_found

# router-level errors
req GET /no/such/route 404 '' not_found
req DELETE /healthz 405 '' method_not_allowed

# cleanup (also proves cascade deletes)
req DELETE "/v1/conversations/$CONV_ID" 204
req GET "/v1/conversations/$CONV_ID" 404 '' not_found
req GET "/v1/runs/$RUN_ID" 404 '' not_found
req DELETE "/v1/agents/$AGENT_ID" 204
req GET "/v1/agents/$AGENT_ID" 404 '' not_found

echo "== $PASS passed, $FAIL failed"
[ "$FAIL" = 0 ]
