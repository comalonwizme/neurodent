package tenant_test

import (
	"context"
	"errors"
	"testing"

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
		{"dash misplaced", "0192f2a45-b6c-7d8e-9f00-112233445566", ""},
		{"non-hex", "0192f2a4-5b6c-7d8e-9f00-11223344556g", ""},
		{"sql injection", "x'; SET app.tenant_id = '1", ""},
		{"too long", "0192f2a4-5b6c-7d8e-9f00-1122334455660", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, err := tenant.Parse(tt.in)
			if tt.want == "" {
				if !errors.Is(err, tenant.ErrInvalidID) || !id.IsZero() {
					t.Errorf("Parse(%q) = %q, %v; want ErrInvalidID", tt.in, id, err)
				}
				return
			}
			if err != nil || id.String() != tt.want {
				t.Errorf("Parse(%q) = %q, %v; want %q", tt.in, id, err, tt.want)
			}
		})
	}
}

func TestContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := tenant.FromContext(ctx); ok {
		t.Error("empty context reports a tenant")
	}
	if _, ok := tenant.FromContext(tenant.WithID(ctx, tenant.ID{})); ok {
		t.Error("zero ID is reported as a tenant")
	}

	id, err := tenant.Parse("0192f2a4-5b6c-7d8e-9f00-112233445566")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := tenant.FromContext(tenant.WithID(ctx, id))
	if !ok || got != id {
		t.Errorf("FromContext() = %v, %v; want %v", got, ok, id)
	}
}
