package queries

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// Queries implements Querier on top of a DBTX (a pool or a transaction).
type Queries struct {
	db DBTX
}

// New returns a Queries bound to db.
func New(db DBTX) *Queries {
	return &Queries{db: db}
}

var _ Querier = (*Queries)(nil)

// sqlFiles maps "<method_snake_case>" to the embedded statement text.
var sqlFiles = loadSQLFiles()

func loadSQLFiles() map[string]string {
	entries, err := fs.ReadDir(sqlFS, "sql")
	if err != nil {
		panic(fmt.Sprintf("queries: read embedded sql dir: %v", err))
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := sqlFS.ReadFile("sql/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("queries: read sql/%s: %v", e.Name(), err))
		}
		files[strings.TrimSuffix(e.Name(), ".sql")] = strings.TrimSpace(string(b))
	}
	return files
}

// mustSQL returns the embedded statement sql/<name>.sql or panics. It runs at
// package init, so a missing or empty file fails before any query is issued.
func mustSQL(name string) string {
	s, ok := sqlFiles[name]
	if !ok {
		panic(fmt.Sprintf("queries: missing embedded file sql/%s.sql", name))
	}
	if s == "" {
		panic(fmt.Sprintf("queries: embedded file sql/%s.sql is empty", name))
	}
	return s
}

// rowScanner is satisfied by pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// wrap maps pgx.ErrNoRows to ErrNotFound and adds operation context to any
// other error. A nil error stays nil.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("queries: %s: %w", op, err)
}

// affected returns ErrNotFound when a write matched no row.
func affected(op string, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// collect drains rows into a non-nil slice.
func collect[T any](rows pgx.Rows, op string, scan func(rowScanner) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, wrap(op, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

var qPing = mustSQL("ping")

// Ping runs a trivial query to prove the database is reachable.
func (q *Queries) Ping(ctx context.Context) error {
	var one int
	return wrap("ping", q.db.QueryRow(ctx, qPing).Scan(&one))
}

// InTx runs fn in a transaction bound to a new Queries. It commits when fn
// returns nil and rolls back when fn returns an error or panics (the panic
// keeps propagating).
func (q *Queries) InTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("queries: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Use a context that survives cancellation so the rollback still runs.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err := fn(&Queries{db: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("queries: commit tx: %w", err)
	}
	committed = true
	return nil
}
