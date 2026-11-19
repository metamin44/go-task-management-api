package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeTx satisfies pgx.Tx via interface embedding; only identity is tested,
// no database connection is involved.
type fakeTx struct{ pgx.Tx }

func TestQuerierFromContext(t *testing.T) {
	pool := &pgxpool.Pool{}

	t.Run("no_tx_uses_pool", func(t *testing.T) {
		q := querierFrom(context.Background(), pool)
		if q != querier(pool) {
			t.Error("without a transaction the pool must be used")
		}
	})

	t.Run("tx_in_context_wins", func(t *testing.T) {
		tx := fakeTx{}
		ctx := context.WithValue(context.Background(), txKey{}, pgx.Tx(tx))
		q := querierFrom(ctx, pool)
		if _, ok := q.(fakeTx); !ok {
			t.Error("transaction from context must take precedence over the pool")
		}
	})
}
