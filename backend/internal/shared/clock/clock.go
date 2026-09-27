// Package clock — источник текущего времени (ADR-0012). В domain/ и app/
// модулей time.Now() запрещён тестом архитектуры: время приходит через
// Clock, и тесты управляют им без sleep.
package clock

import (
	"sync"
	"time"
)

// Clock возвращает текущее время.
type Clock interface {
	Now() time.Time
}

// Real — системные часы. Время в UTC: часовой пояс — свойство
// представления (клиники), а не хранения.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Manual — часы для тестов: стоят, пока их не переведут.
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

// NewManual создаёт часы, показывающие t.
func NewManual(t time.Time) *Manual { return &Manual{now: t.UTC()} }

// Now возвращает установленное время.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Set переводит часы на t.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
}

// Advance сдвигает часы на d.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
