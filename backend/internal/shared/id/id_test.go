package id_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/comalonwizme/neurodent/backend/internal/shared/id"
)

func TestNew_IsV7AndIncreasing(t *testing.T) {
	prev := id.New()
	for range 1000 {
		next := id.New()
		s := next.String()
		if s[14] != '7' {
			t.Fatalf("%s is not UUIDv7", s)
		}
		if next.String() <= prev.String() {
			t.Fatalf("ids are not increasing: %s then %s", prev, next)
		}
		prev = next
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want string // "" — ошибка
	}{
		{"0192f2a4-5b6c-7d8e-9f00-112233445566", "0192f2a4-5b6c-7d8e-9f00-112233445566"},
		{"0192F2A4-5B6C-7D8E-9F00-AABBCCDDEEFF", "0192f2a4-5b6c-7d8e-9f00-aabbccddeeff"},
		{"{0192f2a4-5b6c-7d8e-9f00-112233445566}", ""},
		{"urn:uuid:0192f2a4-5b6c-7d8e-9f00-112233445566", ""},
		{"0192f2a45b6c7d8e9f00112233445566", ""},
		{"0192f2a4-5b6c-7d8e-9f00-11223344556g", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, err := id.Parse(tt.in)
		if tt.want == "" {
			if !errors.Is(err, id.ErrInvalid) {
				t.Errorf("Parse(%q) = %v, %v; want ErrInvalid", tt.in, got, err)
			}
			continue
		}
		if err != nil || got.String() != tt.want {
			t.Errorf("Parse(%q) = %v, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestJSONRoundTripAndZero(t *testing.T) {
	var zero id.ID
	if !zero.IsZero() || id.New().IsZero() {
		t.Error("IsZero is wrong")
	}
	in := struct{ ID id.ID }{ID: id.New()}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ ID id.ID }
	if err := json.Unmarshal(b, &out); err != nil || out != in {
		t.Errorf("round trip: %s → %+v, %v", b, out, err)
	}
	if err := json.Unmarshal([]byte(`{"ID":"{0192f2a4-5b6c-7d8e-9f00-112233445566}"}`), &out); err == nil {
		t.Error("non-canonical id accepted from JSON")
	}
}
