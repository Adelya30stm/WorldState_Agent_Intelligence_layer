package database

import (
	"context"
	"database/sql"
	"fmt"
)

// BeginTx starts a caller-owned transaction on the connection used by q.
func (q *Queries) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	db, ok := q.db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("database: queries are already transaction-bound")
	}
	return db.BeginTx(ctx, opts)
}
