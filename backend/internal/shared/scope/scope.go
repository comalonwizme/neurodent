// Package scope — явный scope транзакции (ADR-0011, ADR-0012): в какой
// клинике и от чьего имени выполняется работа с БД.
//
// Пакет не знает о БД: scope собирают use cases модулей, которым нельзя
// импортировать platform/*. platform/postgres превращает его в параметры
// транзакции (SET LOCAL app.tenant_id, app.actor_kind, app.actor_id,
// app.patient_id), которые читают RLS-политики.
//
// Нулевой Scope невалиден: забытый scope — ошибка до BEGIN, а не
// «ноль строк» или запись без клиники.
package scope

import (
	"errors"
	"fmt"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

// ErrInvalid — scope не задан или собран из несовместимых частей.
var ErrInvalid = errors.New("invalid transaction scope")

// Kind — вид субъекта. Строковые значения (String) — контракт с
// RLS-политиками и CHECK-ограничениями журнала аудита.
type Kind uint8

// Виды субъекта. Нулевое значение — «субъект не задан».
const (
	kindUnset Kind = iota
	// KindStaff — сотрудник клиники.
	KindStaff
	// KindPatientResolve — пациент на шаге разрешения карточки: видит
	// только свои карточки и карточки подопечных (ADR-0011 п. 5).
	KindPatientResolve
	// KindPatient — пациент (или опекун) с выбранной карточкой.
	KindPatient
	// KindSystem — фоновая работа без пользователя.
	KindSystem
	// KindUser — пользователь вне клиники: профиль, сессии.
	KindUser
	// KindAnonymous — до аутентификации: вход, регистрация.
	KindAnonymous
)

// String возвращает значение app.actor_kind.
func (k Kind) String() string {
	switch k {
	case KindStaff:
		return "staff"
	case KindPatientResolve:
		return "patient_resolve"
	case KindPatient:
		return "patient"
	case KindSystem:
		return "system"
	case KindUser:
		return "user"
	case KindAnonymous:
		return "anonymous"
	default:
		return ""
	}
}

// Actor — субъект транзакции. Собирается только конструкторами.
type Actor struct {
	kind    Kind
	user    id.ID
	patient id.ID
}

// Staff — сотрудник клиники.
func Staff(user id.ID) Actor { return Actor{kind: KindStaff, user: user} }

// PatientResolve — пациент на шаге поиска своей карточки в клинике.
func PatientResolve(user id.ID) Actor { return Actor{kind: KindPatientResolve, user: user} }

// Patient — пользователь user работает с карточкой patient: своей или
// подопечного. Право на карточку проверяется на шаге PatientResolve.
func Patient(user, patient id.ID) Actor {
	return Actor{kind: KindPatient, user: user, patient: patient}
}

// System — фоновая работа без пользователя.
func System() Actor { return Actor{kind: KindSystem} }

// User — пользователь вне клиники.
func User(user id.ID) Actor { return Actor{kind: KindUser, user: user} }

// Anonymous — субъект до аутентификации.
func Anonymous() Actor { return Actor{kind: KindAnonymous} }

// Kind возвращает вид субъекта.
func (a Actor) Kind() Kind { return a.kind }

// UserID возвращает пользователя; ok = false для system и anonymous.
func (a Actor) UserID() (id.ID, bool) { return a.user, !a.user.IsZero() }

// PatientID возвращает карточку пациента; ok = false для всех, кроме patient.
func (a Actor) PatientID() (id.ID, bool) { return a.patient, !a.patient.IsZero() }

// Scope — клиника (или её отсутствие) и субъект. Сравнимо через ==:
// вложенная транзакция присоединяется только к тому же scope.
type Scope struct {
	inTenant bool
	tenant   tenant.ID
	actor    Actor
}

// Tenant — работа внутри клиники.
func Tenant(t tenant.ID, a Actor) Scope { return Scope{inTenant: true, tenant: t, actor: a} }

// Global — работа вне клиники (cell-global таблицы). Субъект обязателен:
// журналу аудита нужен субъект каждого события.
func Global(a Actor) Scope { return Scope{actor: a} }

// TenantID возвращает клинику; ok = false для global-scope.
func (s Scope) TenantID() (tenant.ID, bool) { return s.tenant, s.inTenant }

// Actor возвращает субъект.
func (s Scope) Actor() Actor { return s.actor }

// String — для сообщений об ошибках: виды и клиника, без пользователей.
func (s Scope) String() string {
	if s.inTenant {
		return fmt.Sprintf("tenant(%s, %s)", s.tenant, s.actor.kind)
	}
	return fmt.Sprintf("global(%s)", s.actor.kind)
}

// Validate проверяет, что scope собран и части совместимы.
func (s Scope) Validate() error {
	a := s.actor
	if a.kind == kindUnset {
		return fmt.Errorf("%w: scope is not set", ErrInvalid)
	}
	if s.inTenant {
		if s.tenant.IsZero() {
			return fmt.Errorf("%w: tenant scope without tenant", ErrInvalid)
		}
		switch a.kind {
		case KindStaff, KindPatientResolve, KindPatient, KindSystem:
		default:
			return fmt.Errorf("%w: actor %s is not allowed inside a clinic", ErrInvalid, a.kind)
		}
	} else {
		switch a.kind {
		case KindUser, KindAnonymous, KindSystem:
		default:
			return fmt.Errorf("%w: actor %s is only allowed inside a clinic", ErrInvalid, a.kind)
		}
	}
	switch a.kind {
	case KindStaff, KindPatientResolve, KindPatient, KindUser:
		if a.user.IsZero() {
			return fmt.Errorf("%w: actor %s without user", ErrInvalid, a.kind)
		}
	default:
	}
	if a.kind == KindPatient && a.patient.IsZero() {
		return fmt.Errorf("%w: patient actor without patient", ErrInvalid)
	}
	return nil
}
