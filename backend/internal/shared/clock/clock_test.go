package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

func TestReal_IsUTC(t *testing.T) {
	if loc := clock.Real().Now().Location(); loc != time.UTC {
		t.Errorf("Real().Now() location = %v, want UTC", loc)
	}
}

func TestManual(t *testing.T) {
	start := time.Date(2026, 9, 27, 10, 0, 0, 0, time.FixedZone("ALMT", 5*3600))
	m := clock.NewManual(start)
	if got := m.Now(); !got.Equal(start) || got.Location() != time.UTC {
		t.Errorf("Now() = %v, want %v in UTC", got, start)
	}
	m.Advance(90 * time.Second)
	if got := m.Now(); !got.Equal(start.Add(90 * time.Second)) {
		t.Errorf("after Advance: %v", got)
	}
	later := start.Add(time.Hour)
	m.Set(later)
	if got := m.Now(); !got.Equal(later) {
		t.Errorf("after Set: %v", got)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			m.Advance(time.Second)
			_ = m.Now()
		})
	}
	wg.Wait()
	if got := m.Now(); !got.Equal(later.Add(8 * time.Second)) {
		t.Errorf("concurrent Advance: %v", got)
	}
}
