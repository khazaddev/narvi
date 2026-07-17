package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a pgx connection pool for dsn. It is the single place
// stores in this package get a *pgxpool.Pool from — no other constructor
// in this package opens its own connection.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}
	return pool, nil
}
