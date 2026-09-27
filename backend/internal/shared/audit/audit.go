// Package audit — порт журнала аудита для use cases (ADR-0014): типы
// события и интерфейс Recorder. Реализация на Postgres — platform/audit;
// её подставляет internal/app, так что app и ports модулей пишут события,
// не импортируя platform.
//
// В событии нет ни одного поля «строка по выбору вызывающего»: коды
// проверяются по формату, ресурс — UUID. Клинику и субъект события
// вызывающий не передаёт — они берутся из scope транзакции.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
)

// Recorder пишет события журнала.
type Recorder interface {
	// Record пишет событие в текущей транзакции: событие и действие
	// коммитятся или откатываются вместе.
	Record(ctx context.Context, ev Event) error
	// Do выполняет fn в транзакции со scope sc и пишет событие: success в
	// той же транзакции или, после отката, denied/failed с тем же scope.
	// Возвращает ошибку fn. Внутри другой транзакции — ошибка.
	Do(ctx context.Context, sc scope.Scope, ev Event, fn func(ctx context.Context) error) error
}

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

// OutcomeOf классифицирует ошибку действия для журнала.
func OutcomeOf(err error) Outcome {
	switch apperr.KindOf(err) {
	case apperr.Forbidden, apperr.Unauthenticated:
		return Denied
	default:
		return Failed
	}
}
