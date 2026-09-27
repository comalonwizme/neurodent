package scope_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/scope"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

func TestValidate(t *testing.T) {
	clinic := tenant.FromID(id.New())
	user, patient := id.New(), id.New()
	var zeroID id.ID

	valid := []struct {
		name string
		s    scope.Scope
	}{
		{"staff", scope.Tenant(clinic, scope.Staff(user))},
		{"patient resolve", scope.Tenant(clinic, scope.PatientResolve(user))},
		{"patient", scope.Tenant(clinic, scope.Patient(user, patient))},
		{"tenant system", scope.Tenant(clinic, scope.System())},
		{"user", scope.Global(scope.User(user))},
		{"anonymous", scope.Global(scope.Anonymous())},
		{"global system", scope.Global(scope.System())},
	}
	for _, tt := range valid {
		if err := tt.s.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", tt.name, err)
		}
	}

	invalid := []struct {
		name string
		s    scope.Scope
	}{
		{"zero scope", scope.Scope{}},
		{"zero tenant", scope.Tenant(tenant.ID{}, scope.Staff(user))},
		{"zero actor in tenant", scope.Tenant(clinic, scope.Actor{})},
		{"zero actor in global", scope.Global(scope.Actor{})},
		{"user inside clinic", scope.Tenant(clinic, scope.User(user))},
		{"anonymous inside clinic", scope.Tenant(clinic, scope.Anonymous())},
		{"staff outside clinic", scope.Global(scope.Staff(user))},
		{"patient outside clinic", scope.Global(scope.Patient(user, patient))},
		{"staff without user", scope.Tenant(clinic, scope.Staff(zeroID))},
		{"resolve without user", scope.Tenant(clinic, scope.PatientResolve(zeroID))},
		{"patient without patient", scope.Tenant(clinic, scope.Patient(user, zeroID))},
		{"patient without user", scope.Tenant(clinic, scope.Patient(zeroID, patient))},
		{"user without user", scope.Global(scope.User(zeroID))},
	}
	for _, tt := range invalid {
		if err := tt.s.Validate(); !errors.Is(err, scope.ErrInvalid) {
			t.Errorf("%s: Validate() = %v, want ErrInvalid", tt.name, err)
		}
	}
}

func TestEquality(t *testing.T) {
	clinic := tenant.FromID(id.New())
	user := id.New()
	a := scope.Tenant(clinic, scope.Staff(user))
	if a != scope.Tenant(clinic, scope.Staff(user)) {
		t.Error("identical scopes are not equal")
	}
	others := []scope.Scope{
		scope.Tenant(tenant.FromID(id.New()), scope.Staff(user)),
		scope.Tenant(clinic, scope.Staff(id.New())),
		scope.Tenant(clinic, scope.System()),
		scope.Global(scope.User(user)),
	}
	for _, o := range others {
		if a == o {
			t.Errorf("%s == %s", a, o)
		}
	}
}

func TestKindStrings(t *testing.T) {
	want := map[scope.Kind]string{
		scope.KindStaff: "staff", scope.KindPatientResolve: "patient_resolve", scope.KindPatient: "patient",
		scope.KindSystem: "system", scope.KindUser: "user", scope.KindAnonymous: "anonymous",
	}
	for k, s := range want {
		if k.String() != s {
			t.Errorf("%d.String() = %q, want %q", k, k.String(), s)
		}
	}
	var unset scope.Kind
	if unset.String() != "" {
		t.Errorf("unset kind = %q, want empty", unset.String())
	}
}

func TestStringHasNoUserIDs(t *testing.T) {
	user := id.New()
	s := scope.Tenant(tenant.FromID(id.New()), scope.Staff(user)).String()
	if s == "" || strings.Contains(s, user.String()) {
		t.Errorf("String() = %q: must name kinds and clinic, not users", s)
	}
}
