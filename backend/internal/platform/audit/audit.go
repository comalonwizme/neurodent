// Package audit — реализация журнала аудита на Postgres (ADR-0014).
// Типы события и интерфейс — в shared/audit: use cases зависят от них, а
// не от этого пакета.
package audit

import (
	"context"
	"errors"
	"fmt"

	"github.com/comalonwizme/neurodent/backend/internal/platform/audit/auditdb"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	sharedaudit "github.com/comalonwizme/neurodent/backend/internal/shared/audit"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

// Проверка на компиляции: реализация удовлетворяет порту.
var _ sharedaudit.Recorder = (*Recorder)(nil)

// Recorder пишет события.
type Recorder struct {
	db    *postgres.DB
	clock clock.Clock
}

// NewRecorder создаёт Recorder. occurred_at берётся из c.
func NewRecorder(db *postgres.DB, c clock.Clock) *Recorder {
	return &Recorder{db: db, clock: c}
}

// Record пишет событие в текущей транзакции (postgres.Tx; без неё —
// postgres.ErrNoTx). Событие и действие коммитятся или откатываются вместе.
// Клиника и субъект — из scope этой транзакции.
func (r *Recorder) Record(ctx context.Context, ev sharedaudit.Event) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	q, err := postgres.Tx(ctx)
	if err != nil {
		return err
	}
	params := auditdb.InsertEventParams{
		ID:           id.New(),
		OccurredAt:   r.clock.Now(),
		Action:       string(ev.Action),
		ResourceType: string(ev.ResourceType),
		Outcome:      string(ev.Outcome),
	}
	if !ev.ResourceID.IsZero() {
		params.ResourceID = &ev.ResourceID
	}
	if rid := httpx.RequestID(ctx); rid != "" {
		params.RequestID = &rid
	}
	if err := auditdb.New(q).InsertEvent(ctx, params); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// RecordOutcome пишет событие отдельной транзакцией со scope sc — для
// отказов и сбоев, когда бизнес-транзакция откатилась. Внутри транзакции —
// postgres.ErrInsideTx: событие не должно откатиться вместе с ней.
//
// БД проверяет только согласованность события с переданным sc, а не его
// правдивость (ADR-0014). Предпочитайте Do: там scope действия и события
// отказа — одно значение по построению.
func (r *Recorder) RecordOutcome(ctx context.Context, sc scope.Scope, ev sharedaudit.Event) error {
	return r.db.WithinStandaloneTx(ctx, sc, func(ctx context.Context) error {
		return r.Record(ctx, ev)
	})
}

// Do выполняет fn в транзакции со scope sc и пишет событие ev:
//   - fn успешна — событие success в той же транзакции;
//   - fn вернула ошибку — после отката событие denied (apperr forbidden,
//     unauthenticated) или failed отдельной транзакцией с тем же sc.
//
// Возвращает ошибку fn. Do владеет транзакцией: внутри другой транзакции —
// postgres.ErrInsideTx.
func (r *Recorder) Do(ctx context.Context, sc scope.Scope, ev sharedaudit.Event, fn func(ctx context.Context) error) error {
	if postgres.InTx(ctx) {
		return postgres.ErrInsideTx
	}
	ev.Outcome = sharedaudit.Success
	if err := ev.Validate(); err != nil {
		return err
	}
	err := r.db.WithinTx(ctx, sc, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return r.Record(ctx, ev)
	})
	if err == nil {
		return nil
	}
	ev.Outcome = sharedaudit.OutcomeOf(err)
	if recErr := r.RecordOutcome(ctx, sc, ev); recErr != nil {
		return errors.Join(err, fmt.Errorf("record audit outcome: %w", recErr))
	}
	return err
}
