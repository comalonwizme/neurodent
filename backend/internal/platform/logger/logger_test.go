package logger_test

import (
	"bytes"
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
