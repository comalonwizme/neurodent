// Package logger собирает *slog.Logger для процесса и переносит атрибуты
// запроса (request_id и т.п.) из context.Context в каждую запись.
package logger

import (
	"context"
	"io"
	"log/slog"
)

// Format — формат вывода логов.
type Format string

// Поддерживаемые форматы. Неизвестный формат трактуется как JSON: это
// формат прода, и его разберёт сборщик логов.
const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// New создаёт логгер. Атрибуты, положенные в контекст через WithAttrs,
// добавляются к каждой записи, сделанной методами *Context.
func New(w io.Writer, level slog.Level, format Format) *slog.Logger {
	opts := slog.HandlerOptions{
		Level: level,
	}

	var h slog.Handler

	switch format {
	case FormatText:
		h = slog.NewTextHandler(w, &opts)

	case FormatJSON:
		h = slog.NewJSONHandler(w, &opts)

	default:
		h = slog.NewJSONHandler(w, &opts)
	}
	return slog.New(contextHandler{h})
}

type attrsKey struct{}

// WithAttrs возвращает контекст, записи из которого (log.InfoContext(ctx, …))
// получат attrs. Так request_id попадает во все логи запроса, и слоям ниже
// не нужно передавать его руками.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	// Новый слайс, а не append к prev: иначе два дочерних контекста могли бы
	// писать в общий backing array родителя.
	merged := make([]slog.Attr, 0, len(prev)+len(attrs))
	merged = append(merged, prev...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, attrsKey{}, merged)
}

// contextHandler дописывает в запись атрибуты из контекста.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	// ctx не бывает nil: slog.Logger сам подставляет context.Background().
	if attrs, ok := ctx.Value(attrsKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
