package middleware

import (
	"net/http"
	"slices"
	"strings"

	"github.com/comalonwizme/neurodent/backend/internal/platform/httpx"
	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// Параметры CORS (ADR-0015).
const (
	corsAllowMethods = "GET, HEAD, POST, PUT, PATCH, DELETE"
	corsAllowHeaders = "Content-Type, X-Request-ID"
	// X-Request-ID и Retry-After не входят в список заголовков, которые
	// браузер показывает скрипту без разрешения: без них клиент не прочтёт
	// ни id для поддержки, ни время до повтора после 429.
	corsExposeHeaders = "X-Request-ID, Retry-After"
	// 2 часа — потолок Chromium. Кеш preflight после удаления origin из
	// списка закрывает CrossOrigin: он проверяет Origin на каждом запросе.
	corsMaxAge = "7200"
)

// CORS разрешает браузерным клиентам с origins читать ответы API с
// credentials (cookie-сессия, ADR-0005).
//
//   - Разрешённый origin получает Allow-Origin, Allow-Credentials и
//     Expose-Headers на любом ответе, включая ошибки: иначе браузер не
//     покажет клиенту даже текст ошибки.
//   - Preflight отвечается здесь, без хендлера: 204 для разрешённых origin,
//     метода и заголовков, иначе 403 RFC 9457 без CORS-заголовков.
//   - Vary: Origin — на всех ответах: ответ зависит от Origin, и кеш не должен
//     отдать ответ для одного origin другому.
//
// Пустой список (dev) — CORS выключен: заголовков нет, preflight — 403.
func CORS(origins []string) Middleware {
	allowed := slices.Clone(origins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			originOK := origin != "" && slices.Contains(allowed, origin)

			if !isPreflight(r) {
				if originOK {
					h.Set("Access-Control-Allow-Origin", origin)
					h.Set("Access-Control-Allow-Credentials", "true")
					h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
				}
				next.ServeHTTP(w, r)
				return
			}

			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			if !originOK || !preflightMethodOK(r) || !preflightHeadersOK(r) {
				httpx.WriteProblem(w, r, http.StatusForbidden, apperr.Forbidden.String(), "cross-origin request is not allowed")
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

// isPreflight: OPTIONS с Access-Control-Request-Method. Прочие OPTIONS
// идут дальше, в роутер.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func preflightMethodOK(r *http.Request) bool {
	return slices.Contains(strings.Split(corsAllowMethods, ", "), r.Header.Get("Access-Control-Request-Method"))
}

func preflightHeadersOK(r *http.Request) bool {
	allowed := strings.Split(strings.ToLower(corsAllowHeaders), ", ")
	for _, v := range r.Header.Values("Access-Control-Request-Headers") {
		for h := range strings.SplitSeq(v, ",") {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" && !slices.Contains(allowed, h) {
				return false
			}
		}
	}
	return true
}

// CrossOrigin защищает от CSRF (ADR-0015): небезопасные браузерные
// запросы (не GET/HEAD/OPTIONS) с чужого origin отклоняются с 403 RFC 9457.
//
// CORS этого не делает: запрос без тела (POST /sessions/logout) — простой,
// браузер отправит его без preflight вместе с Lax-cookie с соседнего
// поддомена того же сайта. http.CrossOriginProtection смотрит на
// Sec-Fetch-Site (или сравнивает Origin с Host) на каждом запросе.
// Запросы без этих заголовков (мобильное приложение, сервисы) не
// браузерные и пропускаются.
//
// Доверенные origin — тот же список, что у CORS.
func CrossOrigin(trusted []string) (Middleware, error) {
	cop := http.NewCrossOriginProtection()
	for _, o := range trusted {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, err
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, http.StatusForbidden, apperr.Forbidden.String(), "cross-origin request rejected")
	}))
	return cop.Handler, nil
}
