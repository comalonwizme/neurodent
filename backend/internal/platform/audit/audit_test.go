package audit_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/audit"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

func TestEvent_Validate(t *testing.T) {
	ok := audit.Event{Action: "iam.session.created", ResourceType: "iam.session", Outcome: audit.Success}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	tests := []struct {
		name string
		mut  func(*audit.Event)
	}{
		{"free text action", func(e *audit.Event) { e.Action = "Patient Ivanov viewed record" }},
		{"two-segment action", func(e *audit.Event) { e.Action = "iam.created" }},
		{"four-segment action", func(e *audit.Event) { e.Action = "iam.session.created.now" }},
		{"uppercase", func(e *audit.Event) { e.Action = "iam.Session.created" }},
		{"digit first", func(e *audit.Event) { e.Action = "iam.1session.created" }},
		{"empty segment", func(e *audit.Event) { e.Action = "iam..created" }},
		{"too long", func(e *audit.Event) { e.Action = audit.Action("iam.session." + strings.Repeat("a", 100)) }},
		{"resource with verb", func(e *audit.Event) { e.ResourceType = "iam.session.created" }},
		{"resource one segment", func(e *audit.Event) { e.ResourceType = "iam" }},
		{"unknown outcome", func(e *audit.Event) { e.Outcome = "partial" }},
		{"empty outcome", func(e *audit.Event) { e.Outcome = "" }},
	}
	for _, tt := range tests {
		e := ok
		tt.mut(&e)
		if err := e.Validate(); !errors.Is(err, audit.ErrInvalidEvent) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidEvent", tt.name, err)
		}
	}
}

func TestOutcomeOf(t *testing.T) {
	tests := []struct {
		err  error
		want audit.Outcome
	}{
		{apperr.New(apperr.Forbidden, "no"), audit.Denied},
		{fmt.Errorf("x: %w", apperr.New(apperr.Unauthenticated, "")), audit.Denied},
		{apperr.New(apperr.NotFound, ""), audit.Failed},
		{errors.New("db down"), audit.Failed},
	}
	for _, tt := range tests {
		if got := audit.OutcomeOf(tt.err); got != tt.want {
			t.Errorf("OutcomeOf(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

func TestRecord_WithoutTransaction(t *testing.T) {
	r := audit.NewRecorder(nil, clock.Real())
	ev := audit.Event{Action: "iam.session.created", ResourceType: "iam.session", Outcome: audit.Success}
	if err := r.Record(t.Context(), ev); !errors.Is(err, postgres.ErrNoTx) {
		t.Errorf("Record outside tx = %v, want postgres.ErrNoTx", err)
	}
	bad := ev
	bad.Action = "free text"
	if err := r.Record(t.Context(), bad); !errors.Is(err, audit.ErrInvalidEvent) {
		t.Errorf("invalid event is checked before the transaction: %v", err)
	}
}
