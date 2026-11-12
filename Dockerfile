# syntax=docker/dockerfile:1

# ---- build: compile the server -------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- migrate: one-shot job that applies db/migrations --------------------
# Reuses scripts/migrate.sh; with psql on PATH and DATABASE_URL set it talks
# to the database directly instead of going through `docker compose exec`.
FROM postgres:16-alpine AS migrate
RUN apk add --no-cache bash
WORKDIR /app
COPY scripts/migrate.sh ./scripts/migrate.sh
COPY db/migrations ./db/migrations
ENTRYPOINT ["bash", "/app/scripts/migrate.sh"]
CMD ["up"]

# ---- server: the runtime image -------------------------------------------
FROM alpine:3.20 AS server
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server
USER app
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=10 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
