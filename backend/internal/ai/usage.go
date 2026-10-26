package ai

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDailyLimit = errors.New("daily AI limit reached")

// Reserve counts one call for today against limit, atomically: the row is
// only incremented while under the limit, so two clicks at once cannot both
// pass. It returns the calls left after this one. Any DB error is returned
// and the caller must refuse the call (fail closed).
func Reserve(ctx context.Context, pool *pgxpool.Pool, userID int64, limit int) (int, error) {
	var calls int
	err := pool.QueryRow(ctx, `
		INSERT INTO ai_usage (user_id, day, calls) VALUES ($1, current_date, 1)
		ON CONFLICT (user_id, day) DO UPDATE SET calls = ai_usage.calls + 1
		WHERE ai_usage.calls < $2
		RETURNING calls`, userID, limit).Scan(&calls)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrDailyLimit
	}
	if err != nil {
		return 0, err
	}
	return limit - calls, nil
}

// Record adds the tokens a call consumed. Best effort: the call already
// happened and was counted.
func Record(ctx context.Context, pool *pgxpool.Pool, userID int64, in, out int64) error {
	_, err := pool.Exec(ctx, `UPDATE ai_usage SET input_tokens = input_tokens + $2, output_tokens = output_tokens + $3
		WHERE user_id = $1 AND day = current_date`, userID, in, out)
	return err
}

// Prune drops usage older than 90 days; called at startup.
func Prune(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM ai_usage WHERE day < current_date - 90`)
	return err
}
