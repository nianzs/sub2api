package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type iqCheckDrawingRepository struct {
	db *sql.DB
}

func NewIQCheckDrawingRepository(db *sql.DB) service.IQCheckDrawingRepository {
	return &iqCheckDrawingRepository{db: db}
}

func (r *iqCheckDrawingRepository) Save(ctx context.Context, drawing *service.IQCheckDrawing) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO account_iq_check_drawings (account_id, svg, error, model, latency_ms, drawn_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (account_id) DO UPDATE SET svg = EXCLUDED.svg, error = EXCLUDED.error, model = EXCLUDED.model,
 latency_ms = EXCLUDED.latency_ms, drawn_at = EXCLUDED.drawn_at`,
		drawing.AccountID, drawing.SVG, drawing.Error, drawing.Model, drawing.LatencyMs, drawing.DrawnAt)
	return err
}

func (r *iqCheckDrawingRepository) ListByAccountIDs(ctx context.Context, ids []int64) ([]service.IQCheckDrawing, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT account_id, svg, error, model, latency_ms, drawn_at
FROM account_iq_check_drawings WHERE account_id = ANY($1) ORDER BY account_id`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	drawings := make([]service.IQCheckDrawing, 0, len(ids))
	for rows.Next() {
		var drawing service.IQCheckDrawing
		if err := rows.Scan(&drawing.AccountID, &drawing.SVG, &drawing.Error, &drawing.Model, &drawing.LatencyMs, &drawing.DrawnAt); err != nil {
			return nil, err
		}
		drawings = append(drawings, drawing)
	}
	return drawings, rows.Err()
}

// DeleteForDeletedAccounts removes drawings whose account was soft-deleted.
// Accounts are never hard-deleted, so ON DELETE CASCADE alone leaves them behind.
func (r *iqCheckDrawingRepository) DeleteForDeletedAccounts(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM account_iq_check_drawings d
USING accounts a WHERE a.id = d.account_id AND a.deleted_at IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
