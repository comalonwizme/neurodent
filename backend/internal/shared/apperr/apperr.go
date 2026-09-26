// Package apperr — ошибка приложения с категорией, безопасным для клиента
// сообщением и внутренней причиной.
//
// Категория решает, что увидит клиент (транспорт мапит её в статус),
// сообщение — что ему можно показать, причина — что попадёт только в лог.
// Разделение нужно потому, что причина часто содержит то, что клиенту видеть
// нельзя: текст SQL-ошибки, имя таблицы, фрагмент PHI из запроса.
package apperr

import "errors"

// Kind — категория ошибки. Список намеренно короткий и не зависит от
// транспорта: HTTP-статусы, коды gRPC и т.п. выводятся из него снаружи.
type Kind uint8

// Нулевое значение — Internal: ошибка, собранная без явной категории
// (Error{} или забытый Kind), превращается в 500 без деталей, а не
// в ответ с внутренним сообщением. Fail-safe по умолчанию.
const (
	Internal        Kind = iota // сбой на нашей стороне; клиент деталей не получает
	Invalid                     // запрос некорректен: формат, валидация
	NotFound                    // ресурса нет или он не виден вызывающему
	Conflict                    // состояние не позволяет операцию: дубль, версия
	Unauthenticated             // нет валидной сессии
	Forbidden                   // сессия есть, прав нет
	RateLimited                 // превышен лимит частоты
	Unavailable                 // временно недоступно, повтор имеет смысл
)

// String возвращает имя категории в snake_case. Оно стабильно: уходит
// клиенту в поле code и в логи.
func (k Kind) String() string {
	switch k {
	case Internal:
		return "internal"
	case Invalid:
		return "invalid"
	case NotFound:
		return "not_found"
	case Conflict:
		return "conflict"
	case Unauthenticated:
		return "unauthenticated"
	case Forbidden:
		return "forbidden"
	case RateLimited:
		return "rate_limited"
	case Unavailable:
		return "unavailable"
	}
	return "unknown"
}

// Error — ошибка приложения. Поля неэкспортируемые: сообщение и причина
// задаются только через конструкторы, и их нельзя перепутать местами
// при сборке литерала.
type Error struct {
	kind    Kind
	message string
	cause   error
}

// New создаёт ошибку без внутренней причины. message уходит клиенту как есть,
// поэтому в нём не должно быть ни значений из запроса, ни внутренних деталей.
//
// Конструкторы возвращают error, а не *Error: так исключена ловушка с
// типизированным nil (return (*Error)(nil) как непустой error).
func New(kind Kind, message string) error {
	return &Error{kind: kind, message: message}
}

// Wrap создаёт ошибку с внутренней причиной. Причина доступна через
// errors.Is/As и попадает в лог, но никогда — в ответ клиенту.
func Wrap(kind Kind, message string, cause error) error {
	return &Error{kind: kind, message: message, cause: cause}
}

// Error — полное описание для логов: категория, сообщение и причина.
func (e *Error) Error() string {
	s := e.kind.String()
	if e.message != "" {
		s += ": " + e.message
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// Unwrap открывает причину для errors.Is/As.
func (e *Error) Unwrap() error { return e.cause }

// Kind возвращает категорию.
func (e *Error) Kind() Kind { return e.kind }

// Message возвращает сообщение, которое можно показать клиенту.
func (e *Error) Message() string { return e.message }

// KindOf возвращает категорию первой *Error в цепочке. Любая другая ошибка,
// включая nil, — Internal: всё, что не классифицировано явно, считается
// нашим сбоем.
func KindOf(err error) Kind {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.kind
	}
	return Internal
}
