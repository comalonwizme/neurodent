// Package health — пробы liveness и readiness для оркестратора.
//
// Liveness отвечает «процесс жив» и не зависит ни от чего внешнего: если
// БД недоступна, перезапуск пода её не починит, а массовый рестарт всех
// реплик превратит сбой БД в сбой API. Readiness отвечает «можно слать
// трафик»: не в drain и зависимости отвечают.
package health

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	bodyOk          = `{"status":"ok"}`
	bodyDraining    = `{"status":"draining"}`
	bodyUnavailable = `{"status":"unavailable"}`
)

// checkTimeout — бюджет одной проверки readiness. Kubernetes по умолчанию
// ждёт ответа пробы 1s (timeoutSeconds): проверка должна закончиться раньше,
// чтобы 503 ответили мы, а не kubelet засчитал таймаут. Ping БД в том же
// регионе — миллисекунды; 500ms без ответа — БД перегружена, и снять под
// с балансировки правильно.
const checkTimeout = 500 * time.Millisecond

// Check — проверка зависимости для readiness. Fn получает контекст
// с таймаутом checkTimeout.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Probe хранит состояние drain и список проверок readiness.
type Probe struct {
	draining atomic.Bool
	log      *slog.Logger
	checks   []Check
}

// NewProbe создаёт пробу. Проверки выполняются последовательно на каждом
// запросе /readyz.
func NewProbe(log *slog.Logger, checks ...Check) *Probe {
	return &Probe{log: log, checks: checks}
}

// StartDraining переводит readiness в 503 навсегда: процесс останавливается,
// балансировщик должен перестать слать трафик.
func (p *Probe) StartDraining() {
	p.draining.Store(true)
}

// Liveness всегда 200, пока процесс отвечает на HTTP.
func (p *Probe) Liveness(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, bodyOk)
}

// Readiness: 503 при drain или при провале любой проверки. Какая проверка
// упала, пишется в лог, а не в ответ: деталям инфраструктуры в теле
// ответа не место.
func (p *Probe) Readiness(w http.ResponseWriter, r *http.Request) {
	if p.draining.Load() {
		write(w, http.StatusServiceUnavailable, bodyDraining)
		return
	}
	for _, c := range p.checks {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		err := c.Fn(ctx)
		cancel()
		if err != nil {
			p.log.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "err", err)
			write(w, http.StatusServiceUnavailable, bodyUnavailable)
			return
		}
	}
	write(w, http.StatusOK, bodyOk)
}

func write(w http.ResponseWriter, code int, body string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}
