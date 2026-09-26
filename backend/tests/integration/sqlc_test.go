//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/comalonwizme/neurodent/backend/internal/platform/postgres"
	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/tests/integration/probedb"
)

// testSQLC: сгенерированный sqlc-код работает на DBTX текущей транзакции
// (обёртка с флагом занятости), а id.ID кодируется и сканируется pgx.
func testSQLC(t *testing.T, env *pgEnv) {
	ctx := t.Context()
	clinic := newClinic()
	sc := asStaff(clinic)
	rowID := id.New()

	err := env.db.WithinTx(ctx, sc, func(ctx context.Context) error {
		q, err := postgres.Tx(ctx)
		if err != nil {
			return err
		}
		db := probedb.New(q)
		if err := db.InsertProbe(ctx, probedb.InsertProbeParams{ID: rowID, TenantID: clinic.ID(), Note: "via sqlc"}); err != nil {
			return err
		}
		got, err := db.GetProbe(ctx, rowID)
		if err != nil {
			return err
		}
		if got.ID != rowID || got.TenantID != clinic.ID() || got.Note != "via sqlc" {
			t.Errorf("GetProbe = %+v", got)
		}
		notes, err := db.ListProbeNotes(ctx)
		if err != nil {
			return err
		}
		if len(notes) != 1 || notes[0] != "via sqlc" {
			t.Errorf("ListProbeNotes = %v (RLS: only this clinic's rows)", notes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Та же строка не видна из другой клиники — sqlc идёт через тот же RLS.
	err = env.db.WithinTx(ctx, asStaff(newClinic()), func(ctx context.Context) error {
		q, err := postgres.Tx(ctx)
		if err != nil {
			return err
		}
		_, err = probedb.New(q).GetProbe(ctx, rowID)
		return err
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetProbe from another clinic = %v, want pgx.ErrNoRows", err)
	}
}
