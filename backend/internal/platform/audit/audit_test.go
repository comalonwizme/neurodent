package audit_test

import (
	"errors"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/platform/audit"
	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	sharedaudit "github.com/comalonwizme/neurodent/backend/internal/shared/audit"
	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

func TestRecord_WithoutTransaction(t *testing.T) {
	r := audit.NewRecorder(nil, clock.Real())
	ev := sharedaudit.Event{Action: "iam.session.created", ResourceType: "iam.session", Outcome: sharedaudit.Success}
	if err := r.Record(t.Context(), ev); !errors.Is(err, postgres.ErrNoTx) {
		t.Errorf("Record outside tx = %v, want postgres.ErrNoTx", err)
	}
	bad := ev
	bad.Action = "free text"
	if err := r.Record(t.Context(), bad); !errors.Is(err, sharedaudit.ErrInvalidEvent) {
		t.Errorf("invalid event is checked before the transaction: %v", err)
	}
}
