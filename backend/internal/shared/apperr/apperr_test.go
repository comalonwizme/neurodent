package apperr_test

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

func TestKind_String(t *testing.T) {
	tests := []struct {
		kind apperr.Kind
		want string
	}{
		{apperr.Internal, "internal"},
		{apperr.Invalid, "invalid"},
		{apperr.NotFound, "not_found"},
		{apperr.Conflict, "conflict"},
		{apperr.Unauthenticated, "unauthenticated"},
		{apperr.Forbidden, "forbidden"},
		{apperr.RateLimited, "rate_limited"},
		{apperr.Unavailable, "unavailable"},
		{apperr.Kind(200), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestZeroKindIsInternal(t *testing.T) {
	var k apperr.Kind
	if k != apperr.Internal {
		t.Fatalf("zero Kind = %v, want internal (fail-safe default)", k)
	}
	if got := apperr.KindOf(&apperr.Error{}); got != apperr.Internal {
		t.Errorf("KindOf(&Error{}) = %v, want internal", got)
	}
}

func TestError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"kind only", apperr.New(apperr.NotFound, ""), "not_found"},
		{"kind and message", apperr.New(apperr.NotFound, "patient not found"), "not_found: patient not found"},
		{"with cause", apperr.Wrap(apperr.Internal, "cannot save", io.ErrUnexpectedEOF), "internal: cannot save: unexpected EOF"},
		{"cause without message", apperr.Wrap(apperr.Unavailable, "", io.EOF), "unavailable: EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrap_CauseReachableButSeparateFromMessage(t *testing.T) {
	cause := errors.New("pq: relation patients does not exist")
	err := fmt.Errorf("load patient: %w", apperr.Wrap(apperr.Internal, "cannot load patient", cause))

	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause")
	}
	e, ok := errors.AsType[*apperr.Error](err)
	if !ok {
		t.Fatal("errors.As does not find *apperr.Error through %w")
	}
	if e.Kind() != apperr.Internal {
		t.Errorf("Kind() = %v, want internal", e.Kind())
	}
	if e.Message() != "cannot load patient" {
		t.Errorf("Message() = %q: the client-facing message must not include the cause", e.Message())
	}
}

func TestKindOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want apperr.Kind
	}{
		{"nil", nil, apperr.Internal},
		{"plain error", errors.New("boom"), apperr.Internal},
		{"direct", apperr.New(apperr.Conflict, "dup"), apperr.Conflict},
		{"wrapped with %w", fmt.Errorf("ctx: %w", apperr.New(apperr.Forbidden, "no")), apperr.Forbidden},
		{"joined", errors.Join(errors.New("x"), apperr.New(apperr.RateLimited, "slow down")), apperr.RateLimited},
		// Внешняя категория побеждает: слой выше осознанно переклассифицировал ошибку.
		{"outermost wins", apperr.Wrap(apperr.Unavailable, "db down", apperr.New(apperr.NotFound, "x")), apperr.Unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := apperr.KindOf(tt.err); got != tt.want {
				t.Errorf("KindOf() = %v, want %v", got, tt.want)
			}
		})
	}
}
