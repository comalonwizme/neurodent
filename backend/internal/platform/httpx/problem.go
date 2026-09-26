package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// Problem — тело ответа об ошибке по RFC 9457 (application/problem+json).
//
// type всегда "about:blank": отдельных страниц с описанием ошибок у нас нет,
// и RFC в этом случае требует, чтобы title совпадал с текстом статуса.
// Машиночитаемая категория — в расширении code, корреляция с логами —
// в request_id.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// CodePayloadTooLarge — code ответа 413. Отдельной категории в apperr для
// него нет: превышение лимита — это Invalid, а 413 — деталь HTTP.
const CodePayloadTooLarge = "payload_too_large"

// StatusOf мапит ошибку в HTTP-статус.
//
// *http.MaxBytesError проверяется до категории: http.MaxBytesReader
// ставится и в middleware, и в DecodeJSON, и где бы он ни сработал,
// клиент должен получить 413, а не 400.
func StatusOf(err error) int {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return http.StatusRequestEntityTooLarge
	}
	switch apperr.KindOf(err) {
	case apperr.Invalid:
		return http.StatusBadRequest
	case apperr.NotFound:
		return http.StatusNotFound
	case apperr.Conflict:
		return http.StatusConflict
	case apperr.Unauthenticated:
		return http.StatusUnauthorized
	case apperr.Forbidden:
		return http.StatusForbidden
	case apperr.RateLimited:
		return http.StatusTooManyRequests
	case apperr.Unavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// WriteError — конечная точка ошибки в хендлере: пишет problem-ответ и,
// для 5xx, логирует ошибку целиком. После WriteError ошибку не возвращают
// и не логируют повторно.
//
// Для 5xx клиент получает только общий текст: даже «безопасное» сообщение
// apperr может выдать внутреннее устройство («база недоступна»), а причина
// 5xx — всегда наша проблема, клиенту в ней нечего исправлять.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status := StatusOf(err)
	code := apperr.KindOf(err).String()
	detail := ""
	if e, ok := errors.AsType[*apperr.Error](err); ok {
		detail = e.Message()
	}
	if status == http.StatusRequestEntityTooLarge {
		code = CodePayloadTooLarge
	}

	if status >= http.StatusInternalServerError {
		log.ErrorContext(r.Context(), "request failed", "status", status, "err", err)
	} else {
		// 4xx — ошибка клиента, в проде (уровень info) не пишется: статус
		// уже есть в access-логе, а текст может содержать ввод клиента.
		log.DebugContext(r.Context(), "request rejected", "status", status, "err", err)
	}

	WriteProblem(w, r, status, code, detail)
}

// WriteProblem пишет problem-ответ. Используется напрямую там, где ошибка
// рождается в транспорте, а не в приложении: 404/405 роутера, таймаут,
// паника. Для status >= 500 detail заменяется общим текстом.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if status >= http.StatusInternalServerError {
		detail = serverErrorDetail(status)
	}
	p := Problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		Code:      code,
		RequestID: RequestID(r.Context()),
	}
	// Problem состоит из строк и int — Marshal не может вернуть ошибку.
	body, _ := json.Marshal(p)

	// Хендлер мог успеть выставить свои заголовки до ошибки; ответ об ошибке
	// не должен их унаследовать (например, Cache-Control: max-age).
	h := w.Header()
	h.Del("Content-Encoding")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Set("Cache-Control", "no-store")
	// Ошибка записи означает, что клиент ушёл: сообщить ему уже нечего,
	// а статус попадёт в access-лог.
	_ = write(w, status, contentTypeProblem, body)
}

func serverErrorDetail(status int) string {
	if status == http.StatusServiceUnavailable {
		return "service temporarily unavailable, retry later"
	}
	return "internal error"
}
