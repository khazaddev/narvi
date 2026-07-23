package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// PromptTemplateStore is a thin, pass-through wrapper around the sqlc-
// generated prompt_templates queries (§18.6: DB-backed, editable prompt
// templates). No caching, no retries, no business rules -- template
// validation (internal/domain/intent.ValidateTemplate) is entirely the
// caller's own responsibility, run BEFORE Upsert.
type PromptTemplateStore struct {
	q *sqlcgen.Queries
}

// NewPromptTemplateStore builds a PromptTemplateStore backed by pool.
func NewPromptTemplateStore(pool *pgxpool.Pool) *PromptTemplateStore {
	return &PromptTemplateStore{q: sqlcgen.New(pool)}
}

// WithTx returns a PromptTemplateStore whose queries run on tx instead of
// the pool this store was built with.
func (s *PromptTemplateStore) WithTx(tx pgx.Tx) *PromptTemplateStore {
	return &PromptTemplateStore{q: s.q.WithTx(tx)}
}

// Get fetches a named template. A pgx.ErrNoRows error means no template
// exists under that name.
func (s *PromptTemplateStore) Get(ctx context.Context, name string) (sqlcgen.PromptTemplate, error) {
	return s.q.GetPromptTemplate(ctx, name)
}

// GetTemplate returns just name's template text -- structurally satisfies
// internal/app/intentclassifier's own narrow TemplateFetcher interface
// (Go interfaces are structural; no import in either direction is needed
// for this method to count), so intentclassifier's own tests can
// substitute an in-memory fake without a real Postgres connection while
// production wiring passes this store directly.
func (s *PromptTemplateStore) GetTemplate(ctx context.Context, name string) (string, error) {
	row, err := s.Get(ctx, name)
	if err != nil {
		return "", err
	}
	return row.Template, nil
}

// Upsert creates or overwrites a named template's text. Callers MUST
// validate template (internal/domain/intent.ValidateTemplate) before
// calling this -- this store performs no validation of its own.
func (s *PromptTemplateStore) Upsert(ctx context.Context, name, template string) (sqlcgen.PromptTemplate, error) {
	return s.q.UpsertPromptTemplate(ctx, sqlcgen.UpsertPromptTemplateParams{
		Name:     name,
		Template: template,
	})
}
