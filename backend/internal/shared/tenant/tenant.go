// Package tenant — идентификатор клиники (tenant) и его перенос через
// context.Context.
//
// Медзапись принадлежит клинике. Код сервиса фильтрует данные по tenant
// (первый рубеж), а Postgres RLS не отдаёт чужие строки, даже если код
// ошибся (второй рубеж). Источник tenant для RLS — этот контекст:
// platform/postgres.WithinTx выставляет его в транзакции.
//
// Пакет отдельный от будущего shared/auth: tenant-контекст нужен и там, где
// пользователя нет (фоновые задачи, консьюмеры очередей).
package tenant

import (
	"context"
	"errors"
	"strings"
)

// ErrInvalidID — строка не является UUID в каноническом виде.
var ErrInvalidID = errors.New("tenant id must be a canonical UUID")

// ID — идентификатор tenant. Создаётся только через Parse, поэтому любой
// непустой ID валиден. Нулевое значение означает «tenant не задан».
type ID struct{ s string }

// Parse проверяет UUID в каноническом виде (8-4-4-4-12, hex) и
// нормализует его в нижний регистр. Нестрогие формы (без дефисов, в
// фигурных скобках) не принимаются: tenant id сравнивается как строка
// в логах и аудите, у него должна быть одна запись.
func Parse(s string) (ID, error) {
	if len(s) != 36 {
		return ID{}, ErrInvalidID
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ID{}, ErrInvalidID
			}
		default:
			if !isHex(c) {
				return ID{}, ErrInvalidID
			}
		}
	}
	return ID{s: strings.ToLower(s)}, nil
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// String возвращает UUID в нижнем регистре или "" для нулевого ID.
func (id ID) String() string { return id.s }

// IsZero сообщает, что tenant не задан.
func (id ID) IsZero() bool { return id.s == "" }

type ctxKey struct{}

// WithID кладёт tenant в контекст. Нулевой ID не кладётся: контекст без
// tenant и контекст с «пустым» tenant неотличимы, и оба дают ноль строк
// в tenant-таблицах.
func WithID(ctx context.Context, id ID) context.Context {
	if id.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext возвращает tenant из контекста.
func FromContext(ctx context.Context) (ID, bool) {
	id, ok := ctx.Value(ctxKey{}).(ID)
	return id, ok && !id.IsZero()
}
