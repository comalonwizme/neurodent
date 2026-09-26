package logger

import (
	"io"
	"log/slog"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

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
	return slog.New(h)
}
