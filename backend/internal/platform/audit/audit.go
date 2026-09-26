// Package audit — журнал аудита (ADR-0014): кто, в какой клинике, что
// сделал или пытался сделать, с каким ресурсом, когда и чем кончилось.
//
// В событии нет ни одного поля «строка по выбору вызывающего»: коды
// проверяются по формату, ресурс — UUID. Клинику и субъект события
// вызывающий не передаёт — их подставляет БД из параметров транзакции.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/comalonwizme/neurodent/backend/internal/platform/audit/auditdb"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

// ErrInvalidEvent — событие не прошло проверку формата.
var ErrInvalidEvent = errors.New("invalid audit event")

// Action — код действия <модуль>.<ресурс>.<глагол>: iam.session.created.
type Action string

// ResourceType — код типа ресурса <модуль>.<ресурс>: iam.session.
type ResourceType string

// Outcome — чем закончилось действие.
type Outcome string

// Исходы действия.
const (
	Success Outcome = "success"
	Denied  Outcome = "denied" // отказ в доступе: forbidden, unauthenticated
	Failed  Outcome = "failed" // любая другая ошибка
)

// maxCodeLen — то же ограничение, что CHECK в таблице.
const maxCodeLen = 100

// Event — событие журнала.
type Event struct {
	Action       Action
	ResourceType ResourceType
	ResourceID   id.ID // нулевой — событие без конкретного ресурса
	Outcome      Outcome
}

// Validate проверяет формат кодов — те же правила, что CHECK в БД.
func (e Event) Validate() error {
	if !validCode(string(e.Action), 3) {
		return fmt.Errorf("%w: action %q must be <module>.<resource>.<verb>", ErrInvalidEvent, e.Action)
	}
	if !validCode(string(e.ResourceType), 2) {
		return fmt.Errorf("%w: resource type %q must be <module>.<resource>", ErrInvalidEvent, e.ResourceType)
	}
	switch e.Outcome {
	case Success, Denied, Failed:
	default:
		return fmt.Errorf("%w: outcome %q", ErrInvalidEvent, e.Outcome)
	}
	return nil
}

// validCode: ровно segments сегментов через точку, каждый [a-z][a-z0-9_]*.
func validCode(s string, segments int) bool {
	if s == "" || len(s) > maxCodeLen {
		return false
	}
	n := 0
	for part := range strings.SplitSeq(s, ".") {
		n++
		if part == "" || part[0] < 'a' || part[0] > 'z' {
			return false
		}
		for i := 1; i < len(part); i++ {
			if !isCodeChar(part[i]) {
				return false
			}
		}
	}
	return n == segments
}

func isCodeChar(c byte) bool {
	return 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_'
}

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
func (r *Recorder) Record(ctx context.Context, ev Event) error {
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
func (r *Recorder) RecordOutcome(ctx context.Context, sc scope.Scope, ev Event) error {
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
func (r *Recorder) Do(ctx context.Context, sc scope.Scope, ev Event, fn func(ctx context.Context) error) error {
	if postgres.InTx(ctx) {
		return postgres.ErrInsideTx
	}
	ev.Outcome = Success
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
	ev.Outcome = OutcomeOf(err)
	if recErr := r.RecordOutcome(ctx, sc, ev); recErr != nil {
		return errors.Join(err, fmt.Errorf("record audit outcome: %w", recErr))
	}
	return err
}

// OutcomeOf классифицирует ошибку действия для журнала.
func OutcomeOf(err error) Outcome {
	switch apperr.KindOf(err) {
	case apperr.Forbidden, apperr.Unauthenticated:
		return Denied
	default:
		return Failed
	}
}
