package tenant_test

import (
	"errors"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
	"github.com/comalonwizme/neurodent/backend/internal/shared/tenant"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" — ошибка
	}{
		{"lowercase", "0192f2a4-5b6c-7d8e-9f00-112233445566", "0192f2a4-5b6c-7d8e-9f00-112233445566"},
		{"uppercase normalized", "0192F2A4-5B6C-7D8E-9F00-AABBCCDDEEFF", "0192f2a4-5b6c-7d8e-9f00-aabbccddeeff"},
		{"empty", "", ""},
		{"no dashes", "0192f2a45b6c7d8e9f00112233445566", ""},
		{"braces", "{0192f2a4-5b6c-7d8e-9f00-112233445566}", ""},
		{"sql injection", "x'; SET app.tenant_id = '1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tenant.Parse(tt.in)
			if tt.want == "" {
				if !errors.Is(err, tenant.ErrInvalidID) || !got.IsZero() {
					t.Errorf("Parse(%q) = %q, %v; want ErrInvalidID", tt.in, got, err)
				}
				return
			}
			if err != nil || got.String() != tt.want {
				t.Errorf("Parse(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestFromIDAndZero(t *testing.T) {
	var zero tenant.ID
	if !zero.IsZero() || zero.String() != "" {
		t.Errorf("zero tenant: IsZero=%v String=%q", zero.IsZero(), zero.String())
	}
	v := id.New()
	if got := tenant.FromID(v); got.ID() != v || got.String() != v.String() {
		t.Errorf("FromID(%s) = %s", v, got)
	}
}
