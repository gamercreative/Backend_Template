# AGENTS.md

Rules for anyone (human or AI) changing this repo. Read before editing.

## What this is

A Go backend for building an LLM agent: agents (persona + model), conversations,
messages, and runs with a tool-call trace. Go stdlib router (1.22 patterns), pgx,
PostgreSQL, hand-written SQL. No ORM, no web framework.

## Layout and ownership

```
cmd/server/            entrypoint, wiring only
internal/config/       env -> Config
internal/http/         package httpx: error format, JSON helpers, middleware, router, server
internal/handlers/     HTTP <-> service calls. One file per domain.
internal/services/     business rules, validation, the chat flow, tool definitions
internal/queries/      the ONLY package that touches the database
internal/queries/sql/  one .sql file per query
internal/ai/           the ONLY package that talks to a model provider (Anthropic, Ollama, fake)
db/schema.dbml         data model source of truth
db/migrations/         NNNN_name.up.sql / .down.sql
scripts/               migrate.sh, check_queries.sh, smoke.sh
openapi.yaml           HTTP contract
```

## Layering (strict)

`handlers -> services -> queries -> PostgreSQL`, and `services -> ai`.

- Handlers never import `queries` for data access or `pgx` at all. They may use
  the row types (`queries.Agent` ...) only as values passed through from services.
- Services never import `net/http`, `internal/http` or `pgx`.
- Only `internal/queries` (and `cmd/server` for the pool) imports `pgx`.
- Only `internal/ai` imports the Anthropic SDK.
- `scripts/check_queries.sh` fails the build when these are violated.

## Errors: one JSON format

Every non-2xx response has exactly this body:

```json
{"error": {"code": "not_found", "message": "agent not found", "request_id": "..."}}
```

| code                | status | produced by                          |
|---------------------|--------|--------------------------------------|
| bad_request         | 400    | malformed JSON / query params        |
| validation_failed   | 400    | services.Invalid                     |
| not_found           | 404    | services.NotFound, unknown route     |
| method_not_allowed  | 405    | router                               |
| conflict            | 409    | services.Conflict                    |
| internal_error      | 500    | services.Internal, panics            |
| upstream_error      | 502    | services.Upstream (model provider)   |
| timeout             | 504    | request deadline exceeded            |

- Queries return `queries.ErrNotFound` or raw errors.
- Services return `*services.Error` (via `Invalid/NotFound/Conflict/Upstream/Internal`,
  or `FromStore(err, "agent")`) or pass context errors through untouched.
- Handlers call `h.fail(w, r, err)`. Never write an error body any other way and
  never put `err.Error()` of an internal error in a response.

## SQL rules

- One statement per file: `internal/queries/sql/<method_snake_case>.sql`,
  embedded with `//go:embed`. Positional params only. Never build SQL with
  string concatenation or `fmt.Sprintf`.
- List columns explicitly (no `SELECT *`); `RETURNING` the full row on writes.
- `List*` returns a non-nil empty slice. Zero rows on Get/Update/Delete -> `ErrNotFound`.
- Schema change = new migration file + update `db/schema.dbml` + `models.go`.
- Run `make check-queries` after touching any `.sql` file.

## HTTP conventions

- Routes: `mux.HandleFunc("GET /v1/things/{id}", h.getThing)`; read ids with `r.PathValue("id")`.
- JSON is snake_case. Single resources are returned bare; collections as
  `{"data": [...], "limit": n, "offset": n}` via `writeList`.
- Create -> 201, delete -> 204, everything else -> 200.
- Decode with `h.decode`, paginate with `h.page`, fail with `h.fail`.
- Update `openapi.yaml` in the same change as the handler.

## Chat flow (services.ChatService.SendMessage)

1. Validate content. Load conversation (404) and its agent.
2. In one transaction: create run (`running`), create the user message linked to
   the run, touch the conversation.
3. Load recent history (`ListRecentMessages`, oldest first) and call `ai.Client.Run`
   with the agent's system prompt, model (agent model, else default) and the tools.
4. Success: in one transaction store every tool call, the assistant message and
   `FinishRun(completed)`. Return `ChatResult`.
5. Failure: using `context.WithoutCancel(ctx)` store any partial tool calls and
   `FinishRun(failed, error=...)`, then return `Upstream(err)` (or the context
   error). The user message stays stored.

Only user/assistant text is replayed as history; tool calls of earlier turns are
kept as a trace in `tool_calls`, not re-sent to the model.

## Streaming

`POST /v1/conversations/{id}/messages/stream` is the same turn as the plain
route, answered as server-sent events (`start`, `delta`, `thinking`,
`tool_call`, `tool_result`, then exactly one `done` or `error`).

- `ai.Request.OnEvent` turns streaming on inside a model client; `execTool`
  (internal/ai/loop.go) emits the tool events. Every client must go through it.
- `ChatService.StreamMessage` shares its implementation with `SendMessage`
  and adapts `ai.Event` to `services.ChatEvent`. Persistence is identical.
- The handler opens the stream lazily on the first event, so errors before the
  turn starts are normal JSON errors. After that, a failure is an `error`
  event carrying the standard error body.
- Write SSE only through `httpx.NewSSE` / `Send`.

## Model providers

`AI_PROVIDER` picks the `ai.Client`: `anthropic` (official SDK), `ollama`
(local models, native `/api/chat`, stdlib HTTP) or `fake` (offline echo; a
message of the form `/tool <name> <json>` runs a tool). An agent's `model`
must be a model of the active provider; empty means the provider default.
A new provider is one file in internal/ai implementing `Client` with the
helpers in loop.go, plus a case in cmd/server/main.go.

## Docker

`make docker-up` runs three compose services: `db`, a one-shot `migrate`
(scripts/migrate.sh inside a postgres image) and `app` (the server, built from
the `server` target of the Dockerfile). `app` reads `.env` if it exists, but
`DATABASE_URL`, `PORT` and `OLLAMA_BASE_URL` are set by docker-compose.yml
because localhost inside a container is the container. Ollama stays on the
host (it needs the GPU) and is reached at `host.docker.internal:11434`.
After changing Go code run `make docker-up` again to rebuild.

## Adding things

- **A tool**: add an `ai.Tool` to `services.DefaultTools()` in `internal/services/tools.go`.
  The handler gets raw JSON input and returns a string. Return an error to tell
  the model the call failed. Tools that need data take a store in a constructor.
- **An endpoint**: SQL file + `Querier` method -> service method (+ Store interface)
  -> handler + route -> `openapi.yaml` -> `scripts/smoke.sh`.

## Commands

```
make up             start only PostgreSQL (docker compose)
make migrate        apply db/migrations
make run            run the server on the host
make docker-up      build and run db + migrations + server in Docker
make docker-logs    follow the server logs
make down           stop every container
make test           go test ./...
make check          go build + go vet + go test
make check-queries  validate every .sql file against the live schema + layering rules
make smoke          curl every endpoint against a running server
```

Before you call a change done: `go build ./... && go vet ./... && go test ./...`.

## Style

- Standard library first. Adding a dependency needs a reason in the PR.
- `context.Context` is the first parameter of anything that does I/O.
- Table-driven tests; services are tested against in-memory fakes of their Store
  interface, never a real database.
- Logging is `log/slog`. No `fmt.Println` in server code. Never log secrets or
  full prompts at info level.
