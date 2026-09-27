// Package tenant — идентификатор клиники (tenant).
//
// Медзапись принадлежит клинике. Код сервиса фильтрует данные по tenant
// (первый рубеж), а Postgres RLS не отдаёт чужие строки, даже если код
// ошибся (второй рубеж). В транзакцию tenant попадает только через явный
// scope (shared/scope, ADR-0012), не через контекст.
package tenant

import (
	"errors"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
)

// ErrInvalidID — строка не является UUID в каноническом виде.
var ErrInvalidID = errors.New("tenant id must be a canonical UUID")

// ID — идентификатор клиники (= org.clinics.id). Отдельный тип, чтобы
// id клиники нельзя было передать туда, где ждут id пользователя.
// Нулевое значение означает «tenant не задан».
type ID struct{ v id.ID }

// Parse принимает UUID в каноническом виде (8-4-4-4-12).
func Parse(s string) (ID, error) {
	v, err := id.Parse(s)
	if err != nil {
		return ID{}, ErrInvalidID
	}
	return ID{v: v}, nil
}

// FromID превращает id клиники из модуля org в tenant.
func FromID(v id.ID) ID { return ID{v: v} }

// ID возвращает идентификатор клиники.
func (t ID) ID() id.ID { return t.v }

// String возвращает UUID в нижнем регистре или "" для нулевого ID.
func (t ID) String() string {
	if t.IsZero() {
		return ""
	}
	return t.v.String()
}

// IsZero сообщает, что tenant не задан.
func (t ID) IsZero() bool { return t.v.IsZero() }
