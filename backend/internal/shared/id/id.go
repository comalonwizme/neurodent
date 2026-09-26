// Package id — идентификаторы сущностей (ADR-0012): UUIDv7, которые
// генерирует приложение, а не БД. Id нужен до INSERT — для события аудита,
// ответа клиенту и идемпотентности.
//
// Модули объявляют свои типы поверх ID, чтобы компилятор не дал перепутать
// id пользователя и id клиники:
//
//	type UserID struct{ id.ID }
package id

import (
	"errors"
	"uuid"
)

// ErrInvalid — строка не является UUID в каноническом виде.
var ErrInvalid = errors.New("id must be a canonical UUID")

// ID — идентификатор сущности. Нулевое значение означает «не задан».
type ID uuid.UUID

// New возвращает новый UUIDv7. Значения монотонно растут (кроме случая,
// когда системные часы идут назад), поэтому вставки в B-tree индекс идут
// в конец, а не в случайное место.
//
// UUIDv7 раскрывает время создания с точностью до миллисекунды. Для
// сущностей, где это чувствительно, выбирайте другой генератор явно.
func New() ID { return ID(uuid.NewV7()) }

// Parse принимает только канонический вид 8-4-4-4-12 (регистр любой).
// uuid.Parse допускает и другие формы (в фигурных скобках, urn:uuid:);
// у id в API и логах должна быть одна запись.
func Parse(s string) (ID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return ID{}, ErrInvalid
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return ID{}, ErrInvalid
	}
	return ID(u), nil
}

// String возвращает UUID в нижнем регистре.
func (i ID) String() string { return uuid.UUID(i).String() }

// IsZero сообщает, что id не задан.
func (i ID) IsZero() bool { return i == ID{} }

// MarshalText реализует encoding.TextMarshaler (JSON, логи).
func (i ID) MarshalText() ([]byte, error) { return uuid.UUID(i).MarshalText() }

// UnmarshalText реализует encoding.TextUnmarshaler; принимает только
// канонический вид.
func (i *ID) UnmarshalText(b []byte) error {
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*i = v
	return nil
}
