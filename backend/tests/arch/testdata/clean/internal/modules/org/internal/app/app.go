// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	"context"

	"example.com/fx/internal/modules/iam/contract"
	"example.com/fx/internal/modules/org/internal/domain"
	"example.com/fx/internal/modules/org/internal/ports"
	"example.com/fx/internal/shared/audit"
	"example.com/fx/internal/shared/clock"
	"example.com/fx/internal/shared/scope"
	"example.com/fx/internal/shared/txn"
)

// CreateClinic — пример use case: транзакции и аудит приходят портами из
// shared/, реализации (platform/postgres, platform/audit) подставляет
// internal/app. Сам use case platform не импортирует.
type CreateClinic struct {
	tx      txn.Runner
	audit   audit.Recorder
	clinics ports.Clinics
	iam     contract.Memberships
	clock   clock.Clock
}

// NewCreateClinic собирает use case из портов.
func NewCreateClinic(tx txn.Runner, rec audit.Recorder, clinics ports.Clinics, iam contract.Memberships, c clock.Clock) *CreateClinic {
	return &CreateClinic{tx: tx, audit: rec, clinics: clinics, iam: iam, clock: c}
}

// Run создаёт клинику и членство основателя в одной транзакции с аудитом.
func (u *CreateClinic) Run(ctx context.Context, sc scope.Scope, c domain.Clinic) error {
	ev := audit.Event{Action: "org.clinic.created", ResourceType: "org.clinic", ResourceID: c.ID}
	return u.audit.Do(ctx, sc, ev, func(ctx context.Context) error {
		if err := u.clinics.Create(ctx, c); err != nil {
			return err
		}
		return u.iam.AddFounder(ctx, c.ID)
	})
}
