package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/comalonwizme/neurodent/backend/internal/gen/platformapi"
	"github.com/comalonwizme/neurodent/backend/internal/platform/health"
	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/transport/http/middleware"
)

// maxBodyBytes — общий лимит тела запроса. API принимает только JSON:
// файлы (снимки, документы) идут через pre-signed URL напрямую в object
// storage. Самые большие тела — формы медзаписи, это единицы-десятки КиБ;
// 1 МиБ — запас на порядок. Эндпоинту, которому нужно меньше, лимит
// сужается в DecodeJSON.
const maxBodyBytes = 1 << 20

// routerOptions — то, что роутеру нужно из конфига.
type routerOptions struct {
	handlerTimeout time.Duration
	hsts           bool
	corsOrigins    []string
}

// registerRoutes — единственное место, где маршруты попадают на mux.
// Маршруты генерируются из api/openapi/openapi.yaml (ADR-0013): каждый
// сгенерированный пакет регистрирует свои операции шаблонами с методом
// (405 + Allow на чужой метод; GET обслуживает и HEAD). Модули добавят
// сюда свои HandlerWithOptions.
func registerRoutes(mux *http.ServeMux, log *slog.Logger, probe *health.Probe) {
	platformapi.HandlerWithOptions(platformAPI{probe: probe}, platformapi.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: httpx.ParamErrorHandler(log),
	})
}

// platformAPI реализует сгенерированный интерфейс платформенных эндпоинтов.
type platformAPI struct{ probe *health.Probe }

func (a platformAPI) GetHealthz(w http.ResponseWriter, r *http.Request) { a.probe.Liveness(w, r) }
func (a platformAPI) GetReadyz(w http.ResponseWriter, r *http.Request)  { a.probe.Readiness(w, r) }

// newRouter оборачивает mux цепочкой middleware (ADR-0015, заменяет 0009).
//
// Порядок (снаружи внутрь) и почему именно такой:
//
//  1. RequestID — первым: id нужен всем ниже, в том числе логам паник,
//     access-логу и телам ошибок.
//  2. AccessLog — снаружи всех, кто пишет ответ сам: видит итоговый статус
//     (403, 413, 429, 500, 503) и полную длительность.
//  3. SecurityHeaders — заголовки ставятся до любого ответа.
//  4. Recover — снаружи всех слоёв с логикой (CORS, CrossOrigin, RateLimit,
//     Timeout); снаружи него — только простые обёртки. Timeout переносит
//     панику из goroutine хендлера в goroutine запроса, ловит её Recover.
//  5. CORS — до всех, кто пишет ответ: заголовки ставятся в общую карту
//     раньше, чем кто-то ответит (даже 500 от Recover). Preflight отвечает
//     сам, не доходя до хендлера.
//  6. CrossOrigin (CSRF) — preflight уже отвечен; отвергнутая подделка не
//     доходит до лимитов и хендлера.
//     Место [Auth] (IAM) — здесь, до RateLimit (ключ лимита зависит от
//     клиента, ADR-0016). RateLimit — здесь же, до BodyLimit и Timeout.
//  7. BodyLimit — до Timeout: 413 по Content-Length без goroutine хендлера.
//  8. Timeout — ближе всех к хендлеру: бюджет тратится только на хендлер;
//     буферизация даёт чистый 500 даже после частичной записи.
//  9. routeProblems — 404/405 роутера в формате RFC 9457.
func newRouter(mux *http.ServeMux, log *slog.Logger, o routerOptions) (http.Handler, error) {
	crossOrigin, err := middleware.CrossOrigin(o.corsOrigins)
	if err != nil {
		return nil, fmt.Errorf("cross-origin protection: %w", err)
	}
	return middleware.Chain(routeProblems(mux),
		middleware.RequestID,
		middleware.AccessLog(log, mux),
		middleware.SecurityHeaders(o.hsts),
		middleware.Recover(log),
		middleware.CORS(o.corsOrigins),
		crossOrigin,
		middleware.BodyLimit(maxBodyBytes),
		middleware.Timeout(o.handlerTimeout, log),
	), nil
}

// Коды problem-ответов роутера. Категорий apperr для них нет: это ошибки
// маршрутизации, а не приложения.
const (
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
)

// routeProblems отдаёт 404 и 405 роутера в едином формате. ServeMux пишет
// их через http.Error как text/plain; формат ошибок — контракт API, поэтому
// ответ роутера перехватывается и переписывается.
func routeProblems(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" || r.Method == http.MethodConnect {
			mux.ServeHTTP(w, r)
			return
		}
		// Маршрута нет: это 404, 405 или редирект на очищенный путь.
		// Узнаём, что ответил бы ServeMux, не отправляя ответ клиенту.
		c := &statusCapture{header: make(http.Header)}
		h.ServeHTTP(c, r)
		switch c.status {
		case http.StatusNotFound:
			httpx.WriteProblem(w, r, http.StatusNotFound, codeNotFound, "no route for this path")
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", c.header.Get("Allow"))
			httpx.WriteProblem(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed,
				"method is not allowed for this path")
		default:
			mux.ServeHTTP(w, r)
		}
	})
}

// statusCapture — ResponseWriter, который запоминает статус и заголовки
// и выбрасывает тело.
type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header { return c.header }

func (c *statusCapture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *statusCapture) Write(p []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	return len(p), nil
}
