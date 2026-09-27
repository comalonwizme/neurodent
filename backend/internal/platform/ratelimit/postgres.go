package ratelimit

import (
	"context"
	"fmt"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/platform/ratelimit/ratelimitdb"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

// Postgres — общий для реплик счётчик (platform.rate_limit_counters).
//
// Счётчик пишется в собственной транзакции: откат неудачного входа не
// должен отменять учёт попытки. Внутри открытой транзакции —
// postgres.ErrInsideTx (ADR-0012): присоединиться нельзя, а вторая
// транзакция рядом с первой — второе соединение и риск взаимоблокировки.
type Postgres struct {
	db    *postgres.DB
	clock clock.Clock
}

// NewPostgres создаёт лимитер на Postgres. Окна считаются по часам
// приложения: при расхождении часов реплик (NTP) границы окон сдвигаются
// на миллисекунды, это допустимо.
func NewPostgres(db *postgres.DB, c clock.Clock) *Postgres {
	return &Postgres{db: db, clock: c}
}

// Allow учитывает запрос и решает, пропустить ли его.
func (p *Postgres) Allow(ctx context.Context, pol Policy, key Key) (Decision, error) {
	now := p.clock.Now()
	start, end := window(now, pol.Window)
	var hits int32
	err := p.db.WithinStandaloneTx(ctx, scope.Global(scope.System()), func(ctx context.Context) error {
		q, err := postgres.Tx(ctx)
		if err != nil {
			return err
		}
		hits, err = ratelimitdb.New(q).Hit(ctx, ratelimitdb.HitParams{Key: key[:], WindowStart: start, ExpiresAt: end})
		return err
	})
	if err != nil {
		return Decision{}, fmt.Errorf("rate limit %s: %w", pol.Name, err)
	}
	return decide(int(hits), pol, now, end), nil
}

// DeleteExpired удаляет не больше batch истёкших окон и возвращает, сколько
// удалено. Фоновую очистку запускает тот, кто пользуется лимитером (IAM).
func (p *Postgres) DeleteExpired(ctx context.Context, batch int32) (int64, error) {
	var n int64
	err := p.db.WithinStandaloneTx(ctx, scope.Global(scope.System()), func(ctx context.Context) error {
		q, err := postgres.Tx(ctx)
		if err != nil {
			return err
		}
		n, err = ratelimitdb.New(q).DeleteExpired(ctx, ratelimitdb.DeleteExpiredParams{Now: p.clock.Now(), BatchSize: batch})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired rate limit windows: %w", err)
	}
	return n, nil
}
