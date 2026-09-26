// Package httpx — общие HTTP-примитивы для хендлеров и middleware: запись
// JSON, строгое чтение JSON и единый формат ошибок (RFC 9457).
//
// Пакет ничего не знает о маршрутах и модулях. Он — единственное место,
// где apperr превращается в HTTP-статус, поэтому формат ошибок у всех
// эндпоинтов одинаковый.
package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Заголовки ответа, общие для пакета и middleware.
const (
	HeaderRequestID    = "X-Request-ID"
	contentTypeJSON    = "application/json"
	contentTypeProblem = "application/problem+json"
)

type requestIDKey struct{}

// WithRequestID кладёт идентификатор запроса в контекст. Вызывает его
// middleware request-id; остальные только читают.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID возвращает идентификатор запроса или "", если его нет.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WriteJSON сериализует v и пишет ответ со статусом status.
//
// Сериализация идёт в память до записи заголовков: если v не сериализуется,
// ответ ещё не начат и вызывающий может отдать нормальную ошибку вместо
// обрезанного 200. Ошибка возвращается, а не логируется: решение за
// вызывающим (обычно — WriteError).
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode json response: %w", err)
	}
	return write(w, status, contentTypeJSON, body)
}

func write(w http.ResponseWriter, status int, contentType string, body []byte) error {
	body = append(body, '\n')
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}
