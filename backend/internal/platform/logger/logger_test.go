package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/logger"
)

func TestNew_JSON(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, slog.LevelInfo, logger.FormatJSON).Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", rec["msg"])
	}
	if rec["k"] != "v" {
		t.Errorf("k = %v, want v", rec["k"])
	}
}

func TestNew_Text(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, slog.LevelInfo, logger.FormatText).Info("hello")

	out := buf.String()
	if out == "" {
		t.Fatal("nothing was written")
	}
	if json.Valid(buf.Bytes()) {
		t.Errorf("text format produced JSON: %s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "msg=hello") {
		t.Errorf("unexpected text output: %s", out)
	}
}

func TestNew_LevelFiltersDebug(t *testing.T) {
	for _, format := range []logger.Format{logger.FormatJSON, logger.FormatText} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger.New(&buf, slog.LevelInfo, format).Debug("hidden")
			if buf.Len() != 0 {
				t.Errorf("debug record written at info level: %s", buf.String())
			}
		})
	}
}

func TestNew_UnknownFormatFallsBackToJSON(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, slog.LevelInfo, logger.Format("yaml")).Info("hello")

	if !json.Valid(buf.Bytes()) {
		t.Errorf("unknown format did not fall back to JSON: %s", buf.String())
	}
}

func TestWithAttrs_AddsContextAttrsToRecords(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).With("component", "test")

	parent := logger.WithAttrs(context.Background(), slog.String("request_id", "r1"))
	child := logger.WithAttrs(parent, slog.String("tenant", "t1"))
	sibling := logger.WithAttrs(parent, slog.String("tenant", "t2"))

	log.InfoContext(child, "hello")
	log.InfoContext(sibling, "hello")
	log.Info("no context")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	want := []map[string]any{
		{"request_id": "r1", "tenant": "t1", "component": "test"},
		{"request_id": "r1", "tenant": "t2", "component": "test"},
		{"component": "test"},
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		for k, v := range want[i] {
			if rec[k] != v {
				t.Errorf("line %d: %s = %v, want %v", i, k, rec[k], v)
			}
		}
		if _, ok := rec["request_id"]; i == 2 && ok {
			t.Errorf("line %d: request_id leaked into a record without context", i)
		}
	}
}

func TestWithAttrs_SurvivesWithGroupAndNilContext(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).WithGroup("g")
	ctx := logger.WithAttrs(context.Background(), slog.String("request_id", "r1"))
	log.InfoContext(ctx, "grouped")

	var rec struct {
		G map[string]any `json:"g"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.G["request_id"] != "r1" {
		t.Errorf("request_id lost after WithGroup: %s", buf.String())
	}

	buf.Reset()
	var nilCtx context.Context
	log.InfoContext(nilCtx, "nil ctx") // slog подставляет Background: не паникует
	if !json.Valid(buf.Bytes()) {
		t.Errorf("nil context record is not JSON: %s", buf.String())
	}
}
