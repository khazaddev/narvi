package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a pgx connection pool for dsn. It is the single place
// stores in this package get a *pgxpool.Pool from — no other constructor
// in this package opens its own connection.
//
// MaxConns is left entirely to dsn/pgxpool's own resolution (an explicit
// "pool_max_conns" DSN param, or pgxpool's own max(4, runtime.NumCPU())
// default otherwise) -- callers that need an explicit override (the real
// production wiring, cmd/control-plane/main.go) use NewPoolWithMaxConns
// instead, which this function delegates to unchanged so every existing
// caller here (tests included) keeps its exact current behavior.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return NewPoolWithMaxConns(ctx, dsn, 0)
}

// NewPoolWithMaxConns is NewPool with an optional MaxConns override:
// maxConns == 0 means "no override" (identical behavior to NewPool above),
// a positive value overrides whatever dsn/pgxpool would otherwise resolve
// MaxConns to. See platform.Config.DBPoolMaxConns's own doc comment for why
// the real production wiring needs this override.
func NewPoolWithMaxConns(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse pool config: %w", err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}
	return pool, nil
}
