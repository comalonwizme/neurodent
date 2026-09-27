package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/shared/clock"
)

// Memory — счётчик в памяти реплики. При N репликах эффективный лимит
// ≤ N × Limit: для грубой защиты этого достаточно.
//
// Число ключей ограничено maxKeys: перебор адресов не раздувает память.
// При переполнении сначала удаляются истёкшие окна; если места всё равно
// нет, новый ключ пропускается без учёта (fail open — тот, кто перебирает
// адреса, лимит по адресу всё равно обходит; от такой атаки защищает
// балансировщик или WAF).
type Memory struct {
	clock   clock.Clock
	maxKeys int

	mu      sync.Mutex
	entries map[Key]*memEntry
}

type memEntry struct {
	start, end time.Time
	hits       int
}

// NewMemory создаёт лимитер в памяти.
func NewMemory(c clock.Clock, maxKeys int) *Memory {
	return &Memory{clock: c, maxKeys: maxKeys, entries: make(map[Key]*memEntry)}
}

// Allow учитывает запрос и решает, пропустить ли его.
func (m *Memory) Allow(_ context.Context, p Policy, key Key) (Decision, error) {
	now := m.clock.Now()
	start, end := window(now, p.Window)

	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		if len(m.entries) >= m.maxKeys {
			m.sweep(now)
		}
		if len(m.entries) >= m.maxKeys {
			return Decision{Allowed: true}, nil
		}
		e = &memEntry{}
		m.entries[key] = e
	}
	if !e.start.Equal(start) {
		e.start, e.end, e.hits = start, end, 0
	}
	e.hits++
	return decide(e.hits, p, now, end), nil
}

func (m *Memory) sweep(now time.Time) {
	for k, e := range m.entries {
		if !e.end.After(now) {
			delete(m.entries, k)
		}
	}
}

// Len — число отслеживаемых ключей (для тестов и отладки).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
