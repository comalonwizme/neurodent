package health

import (
	"io"
	"net/http"
	"sync/atomic"
)

const (
	bodyOk       = `{"status":"ok"}`
	bodyDraining = `{"status":"draining"}`
)

type Probe struct {
	draining atomic.Bool
}

func NewProbe() *Probe {
	return &Probe{}
}

func (p *Probe) StartDraining() {
	p.draining.Store(true)
}

func (p *Probe) Liveness(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, bodyOk)
}

func (p *Probe) Readiness(w http.ResponseWriter, _ *http.Request) {
	if p.draining.Load() {
		write(w, http.StatusServiceUnavailable, bodyDraining)
		return
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
