// Package config загружает и валидирует конфигурацию процесса из переменных
// окружения. Конфиг читается один раз на старте; любая ошибка — отказ старта.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Env — окружение, в котором запущен процесс. Значение типа Env можно получить
// только через parseEnv, поэтому после Load() невалидного Env не существует.
type Env string

// Поддерживаемые окружения. От окружения зависят уровень безопасности
// (HSTS, TLS до БД, запрет debug-логов в prod) и задержка drain.
const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// Имена переменных окружения. Префикс обязателен: без него мы сталкиваемся
// с переменными, которые инжектит платформа (например, Kubernetes *_PORT).
const (
	keyEnv               = "NEURODENT_ENV"
	keyHTTPAddr          = "NEURODENT_HTTP_ADDR"
	keyReadHeaderTimeout = "NEURODENT_READ_HEADER_TIMEOUT"
	keyReadTimeout       = "NEURODENT_READ_TIMEOUT"
	keyWriteTimeout      = "NEURODENT_WRITE_TIMEOUT"
	keyHandlerTimeout    = "NEURODENT_HANDLER_TIMEOUT"
	keyIdleTimeout       = "NEURODENT_IDLE_TIMEOUT"
	keyShutdownTimeout   = "NEURODENT_SHUTDOWN_TIMEOUT"
	keyDrainDelay        = "NEURODENT_DRAIN_DELAY"
	keyLogLevel          = "NEURODENT_LOG_LEVEL"

	keyDBDSN              = "NEURODENT_DB_DSN"
	keyDBMaxConns         = "NEURODENT_DB_MAX_CONNS"
	keyDBConnectTimeout   = "NEURODENT_DB_CONNECT_TIMEOUT"
	keyDBStatementTimeout = "NEURODENT_DB_STATEMENT_TIMEOUT"
	keyDBMaxConnLifetime  = "NEURODENT_DB_MAX_CONN_LIFETIME"

	keyMigrateDSN = "NEURODENT_MIGRATE_DSN"

	keyCORSAllowedOrigins = "NEURODENT_CORS_ALLOWED_ORIGINS"
)

// Бюджет остановки процесса.
const (
	// Kubernetes по умолчанию шлёт SIGKILL через 30s после SIGTERM
	// (terminationGracePeriodSeconds). Перехватить SIGKILL нельзя.
	orchestratorGracePeriod = 30 * time.Second

	// Запас после http Shutdown на закрытие ресурсов в обратном порядке
	// (пул БД, flush логов и трейсов).
	resourceCloseReserve = 5 * time.Second

	// DrainDelay + ShutdownTimeout обязаны уложиться в этот бюджет.
	stopBudget = orchestratorGracePeriod - resourceCloseReserve
)

// Значения по умолчанию. Допущения, на которых они держатся:
//   - JSON-API без загрузки файлов (файлы идут через pre-signed URL напрямую
//     в object storage), тело запроса — килобайты, максимум ~1 МБ;
//   - перед нами балансировщик/reverse proxy; TLS терминируется на нём;
//   - клиенты — браузеры и мобильные сети. Если прокси НЕ буферизует тело
//     целиком, медленный клиент передаёт тело прямо нам — таймауты чтения
//     рассчитаны на этот худший случай. Решение по буферизации — на уровне деплоя.
const (
	defaultHTTPAddr = "127.0.0.1:8080" // только loopback: dev-сервер не виден в чужом Wi-Fi. В docker/k8s задаём 0.0.0.0:8080 явно.

	// Заголовки — единицы КБ; даже на плохой мобильной сети это доли секунды.
	// 5s отсекает Slowloris и не режет честного клиента с высоким RTT.
	defaultReadHeaderTimeout = 5 * time.Second

	// Заголовки + тело. 1 МБ на ~1 Мбит/с аплинка (слабый 3G) ≈ 8s; 15s — с запасом.
	defaultReadTimeout = 15 * time.Second

	// Хендлер + запись ответа. Timeout-middleware (HandlerTimeout, 10s) меньше
	// этого значения, чтобы клиент получил нормальный 503, а не оборванное соединение.
	defaultWriteTimeout = 15 * time.Second

	// Бюджет хендлера в timeout-middleware. Строго меньше WriteTimeout: после
	// таймаута middleware ещё должен успеть записать 503 до дедлайна соединения.
	// 10s: JSON-эндпоинт, которому нужно больше, делает что-то не то (долгие
	// операции уходят в фон через очередь); 5s запаса до WriteTimeout хватает
	// на запись problem-ответа даже медленному клиенту.
	defaultHandlerTimeout = 10 * time.Second

	// Должен быть БОЛЬШЕ idle-таймаута балансировщика (AWS ALB — 60s, nginx
	// keepalive_timeout — 75s). Если мы закроем keep-alive раньше LB, он может
	// отправить запрос в закрывающееся соединение, и клиент получит 502.
	defaultIdleTimeout = 120 * time.Second

	// >= WriteTimeout: запрос в полёте успевает уложиться в свой лимит.
	// 15s + drain 2s = 17s < stopBudget (25s).
	defaultShutdownTimeout = 15 * time.Second

	// Время, за которое LB замечает /readyz=503 и перестаёт слать трафик.
	// В dev балансировщика нет — ждать незачем.
	defaultDrainDelay    = 2 * time.Second
	defaultDrainDelayDev = 0

	defaultLogLevel = "info"

	// Пул на процесс. max_connections в Postgres по умолчанию 100, и каждое
	// соединение — отдельный процесс с памятью. 10 на реплику API оставляет
	// место для нескольких реплик, мигратора, мониторинга и админского доступа.
	// Хендлер держит соединение только на время транзакции, 10 параллельных
	// транзакций — сотни RPS для коротких запросов.
	defaultDBMaxConns = 10

	// Установка соединения внутри региона — миллисекунды, TLS + SCRAM под
	// нагрузкой — сотни. 5s ловят недоступную БД на старте, не подвешивая его.
	defaultDBConnectTimeout = 5 * time.Second

	// statement_timeout на стороне Postgres. Меньше HandlerTimeout (инвариант):
	// запрос, который переживёт хендлер, — работа впустую, держащая соединение
	// и блокировки. OLTP-запрос дольше 5s — баг или отсутствующий индекс.
	defaultDBStatementTimeout = 5 * time.Second

	// Соединения пересоздаются: после failover (новый primary за тем же DNS),
	// ротации паролей и чтобы backend-процессы не копили память. 30m — ротация
	// заметна за время одного деплоя и не создаёт шторма переподключений.
	defaultDBMaxConnLifetime = 30 * time.Minute
)

// bounds — допустимый диапазон для duration-переменной. Верхняя граница ловит
// опечатки вида WRITE_TIMEOUT=10h, которые молча выключают защиту.
type bounds struct{ min, max time.Duration }

// errEmpty — sentinel-ошибка. Единственное допустимое исключение из правила
// «без глобальных переменных»: её никто не изменяет (как io.EOF в stdlib).
var errEmpty = errors.New("set but empty")

// redacted — то, что видят fmt, slog и json вместо секрета.
const redacted = "[REDACTED]"

// Secret — строка, которая не печатается: fmt (любой глагол), slog и
// encoding/json видят "[REDACTED]". Значение достаётся только явным вызовом
// Reveal — его легко найти grep'ом при ревью.
//
// Структура, а не type Secret string: конверсия string(secret) в обход
// Reveal не компилируется.
type Secret struct{ v string }

// Format реализует fmt.Formatter. String и GoString недостаточно: для %d,
// %o и т.п. fmt их не вызывает и печатает поля структуры через reflection —
// вместе со значением (это поймал TestSecret_NeverPrinted).
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// Reveal возвращает значение секрета. Вызывать только там, где значение
// передаётся потребителю (драйверу БД), и никогда — для логов и ошибок.
func (s Secret) Reveal() string { return s.v }

// String реализует fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString реализует fmt.GoStringer (%#v).
func (Secret) GoString() string { return redacted }

// LogValue реализует slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText реализует encoding.TextMarshaler (json, xml).
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Config — значение, а не указатель: после Load() он иммутабелен
// и передаётся копией.
type Config struct {
	Env               Env
	HTTPAddr          string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	HandlerTimeout    time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	DrainDelay        time.Duration
	LogLevel          slog.Level

	DBDSN              Secret // не логируется даже через LogValue
	DBMaxConns         int32
	DBConnectTimeout   time.Duration
	DBStatementTimeout time.Duration
	DBMaxConnLifetime  time.Duration

	// Origin браузерного клиента (ADR-0015): CORS с credentials и доверенные
	// origin для защиты от CSRF. Пусто — только в dev (Angular dev-server
	// проксирует API, запросы same-origin).
	CORSAllowedOrigins Origins
}

// Origins — список origin вида scheme://host[:port]. Хранится строкой,
// чтобы Config оставался сравнимым (тесты сравнивают его целиком).
type Origins struct{ joined string }

// List возвращает origin по одному.
func (o Origins) List() []string {
	if o.joined == "" {
		return nil
	}
	return strings.Split(o.joined, ",")
}

// LogValue реализует slog.LogValuer по принципу allowlist: в лог попадают
// только явно перечисленные поля. Новое поле (например, токен SMS-шлюза)
// по умолчанию в лог НЕ попадёт.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", string(c.Env)),
		slog.String("http_addr", c.HTTPAddr),
		slog.Duration("read_header_timeout", c.ReadHeaderTimeout),
		slog.Duration("read_timeout", c.ReadTimeout),
		slog.Duration("write_timeout", c.WriteTimeout),
		slog.Duration("handler_timeout", c.HandlerTimeout),
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Duration("drain_delay", c.DrainDelay),
		slog.String("log_level", c.LogLevel.String()),
		slog.Int("db_max_conns", int(c.DBMaxConns)),
		slog.Duration("db_connect_timeout", c.DBConnectTimeout),
		slog.Duration("db_statement_timeout", c.DBStatementTimeout),
		slog.Duration("db_max_conn_lifetime", c.DBMaxConnLifetime),
		slog.String("cors_allowed_origins", c.CORSAllowedOrigins.joined),
	)
}

// Load читает конфиг из окружения процесса.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

// load принимает источник переменных параметром — в тестах это map.
func load(lookup func(string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}

	// Этап 1: каждое поле по отдельности — парсинг и диапазон.
	// ok == false означает «поле невалидно, ошибка уже записана».
	env, envOK := l.env(keyEnv)
	addr, _ := l.addr(keyHTTPAddr, defaultHTTPAddr)
	rht, rhtOK := l.duration(keyReadHeaderTimeout, defaultReadHeaderTimeout, bounds{time.Second, 10 * time.Second})
	rt, rtOK := l.duration(keyReadTimeout, defaultReadTimeout, bounds{time.Second, time.Minute})
	wt, wtOK := l.duration(keyWriteTimeout, defaultWriteTimeout, bounds{time.Second, time.Minute})
	ht, htOK := l.duration(keyHandlerTimeout, defaultHandlerTimeout, bounds{time.Second, time.Minute})
	it, _ := l.duration(keyIdleTimeout, defaultIdleTimeout, bounds{10 * time.Second, 5 * time.Minute})
	st, stOK := l.duration(keyShutdownTimeout, defaultShutdownTimeout, bounds{time.Second, stopBudget})

	drainDefault := defaultDrainDelay
	if envOK && env == EnvDev {
		drainDefault = defaultDrainDelayDev
	}
	dd, ddOK := l.duration(keyDrainDelay, drainDefault, bounds{0, 10 * time.Second})

	lvl, lvlOK := l.logLevel(keyLogLevel, defaultLogLevel)

	dsn := l.secret(keyDBDSN)
	maxConns, _ := l.intRange(keyDBMaxConns, defaultDBMaxConns, 1, 100)
	dbct, _ := l.duration(keyDBConnectTimeout, defaultDBConnectTimeout, bounds{time.Second, 30 * time.Second})
	dbst, dbstOK := l.duration(keyDBStatementTimeout, defaultDBStatementTimeout, bounds{100 * time.Millisecond, time.Minute})
	dblt, _ := l.duration(keyDBMaxConnLifetime, defaultDBMaxConnLifetime, bounds{time.Minute, 24 * time.Hour})

	origins, originsOK := l.origins(keyCORSAllowedOrigins)

	// Этап 2: кросс-полевые инварианты — только если все участники валидны.
	// Иначе одна опечатка порождает каскад ложных ошибок про соседние поля.
	if rhtOK && rtOK && rht > rt {
		l.fail(keyReadHeaderTimeout, fmt.Errorf("must be <= %s", keyReadTimeout))
	}
	if htOK && wtOK && ht >= wt {
		l.fail(keyHandlerTimeout, fmt.Errorf("must be < %s, otherwise the connection is cut before the 503 is written", keyWriteTimeout))
	}
	if stOK && wtOK && st < wt {
		l.fail(keyShutdownTimeout, fmt.Errorf("must be >= %s, otherwise in-flight requests are cut", keyWriteTimeout))
	}
	if stOK && ddOK && st+dd > stopBudget {
		l.fail(keyShutdownTimeout, fmt.Errorf("%s + %s must be <= %s (orchestrator grace period minus close reserve)", keyShutdownTimeout, keyDrainDelay, stopBudget))
	}
	if dbstOK && htOK && dbst >= ht {
		l.fail(keyDBStatementTimeout, fmt.Errorf("must be < %s: a query that outlives the handler is wasted work holding a connection", keyHandlerTimeout))
	}
	if envOK && originsOK && env != EnvDev {
		if origins.joined == "" {
			l.fail(keyCORSAllowedOrigins, errors.New("required outside dev: the browser client needs CORS and CSRF trusted origins"))
		}
		for _, o := range origins.List() {
			if strings.HasPrefix(o, "http://") {
				l.fail(keyCORSAllowedOrigins, fmt.Errorf("origin %q: only https outside dev", o))
			}
		}
	}
	if envOK && lvlOK && env == EnvProd && lvl < slog.LevelInfo {
		l.fail(keyLogLevel, errors.New("levels below info are forbidden in prod: debug logs end up containing PHI"))
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, err
	}

	return Config{
		Env:               env,
		HTTPAddr:          addr,
		ReadHeaderTimeout: rht,
		ReadTimeout:       rt,
		WriteTimeout:      wt,
		HandlerTimeout:    ht,
		IdleTimeout:       it,
		ShutdownTimeout:   st,
		DrainDelay:        dd,
		LogLevel:          lvl,

		DBDSN:              dsn,
		DBMaxConns:         maxConns,
		DBConnectTimeout:   dbct,
		DBStatementTimeout: dbst,
		DBMaxConnLifetime:  dblt,

		CORSAllowedOrigins: origins,
	}, nil
}

// Migrate — конфиг cmd/migrate. Отдельный от Config: мигратору не нужны
// HTTP-настройки, а API не должен знать DSN владельца схемы.
type Migrate struct {
	// Env решает, обязателен ли TLS до БД (вне dev — да) и формат логов.
	Env Env
	// DSN роли-владельца схемы. У API своя роль без прав на DDL.
	DSN Secret
}

// LoadMigrate читает конфиг мигратора из окружения процесса.
func LoadMigrate() (Migrate, error) {
	return loadMigrate(os.LookupEnv)
}

func loadMigrate(lookup func(string) (string, bool)) (Migrate, error) {
	l := &loader{lookup: lookup}
	env, _ := l.env(keyEnv)
	dsn := l.secret(keyMigrateDSN)
	if err := errors.Join(l.errs...); err != nil {
		return Migrate{}, err
	}
	return Migrate{Env: env, DSN: dsn}, nil
}

// loader накапливает ошибки, чтобы оператор увидел их все сразу.
//
// Правило для будущих секретных переменных (DSN, ключи): ошибки парсинга
// стандартных функций содержат исходное значение. Для секретов формируй
// своё сообщение без значения и НЕ оборачивай исходную ошибку через %w.
type loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (l *loader) fail(key string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s: %w", key, err))
}

// optional возвращает значение или дефолт, если переменная не задана.
// Заданная пустая строка — ошибка оператора, а не «используй дефолт».
func (l *loader) optional(key, def string) (string, bool) {
	s, ok := l.lookup(key)
	if !ok {
		return def, true
	}
	if s == "" {
		l.fail(key, errEmpty)
		return "", false
	}
	return s, true
}

func (l *loader) required(key string) (string, bool) {
	s, ok := l.lookup(key)
	if !ok {
		l.fail(key, errors.New("required"))
		return "", false
	}
	if s == "" {
		l.fail(key, errEmpty)
		return "", false
	}
	return s, true
}

// env: от окружения зависит уровень безопасности (cookie Secure, уровень
// логов), поэтому у него нет дефолта.
func (l *loader) env(key string) (Env, bool) {
	s, ok := l.required(key)
	if !ok {
		return "", false
	}
	switch e := Env(s); e {
	case EnvDev, EnvStaging, EnvProd:
		return e, true
	}
	l.fail(key, fmt.Errorf("unknown value %q, want dev|staging|prod", s))
	return "", false
}

func (l *loader) addr(key, def string) (string, bool) {
	s, ok := l.optional(key, def)
	if !ok {
		return "", false
	}
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		l.fail(key, err)
		return "", false
	}
	// SplitHostPort не проверяет порт: ":abc" и ":0" проходят.
	// :0 — случайный порт от ОС, до него не дотянется балансировщик.
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		l.fail(key, fmt.Errorf("port %q must be a number in 1..65535", port))
		return "", false
	}
	return s, true
}

// duration парсит длительность и проверяет диапазон. Положительность —
// свойство поля, поэтому она здесь, а не среди кросс-полевых инвариантов.
func (l *loader) duration(key string, def time.Duration, b bounds) (time.Duration, bool) {
	s, ok := l.lookup(key)
	if !ok {
		return def, true
	}
	if s == "" {
		l.fail(key, errEmpty)
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		l.fail(key, err)
		return 0, false
	}
	if d < b.min || d > b.max {
		l.fail(key, fmt.Errorf("%s is out of range [%s, %s]", d, b.min, b.max))
		return 0, false
	}
	return d, true
}

// secret читает обязательную секретную переменную. Ошибки не содержат
// значения: required и errEmpty его не упоминают, а парсинг DSN делает
// драйвер и возвращает ошибку без %w (см. platform/postgres).
//
// Возвращает только значение: у секретов нет кросс-полевых инвариантов,
// и признак валидности никому не нужен.
func (l *loader) secret(key string) Secret {
	s, _ := l.required(key)
	return Secret{v: s}
}

// intRange парсит целое и проверяет диапазон [lo, hi].
func (l *loader) intRange(key string, def, lo, hi int32) (int32, bool) {
	s, ok := l.lookup(key)
	if !ok {
		return def, true
	}
	if s == "" {
		l.fail(key, errEmpty)
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		l.fail(key, err)
		return 0, false
	}
	if v := int32(n); v < lo || v > hi {
		l.fail(key, fmt.Errorf("%d is out of range [%d, %d]", v, lo, hi))
		return 0, false
	}
	return int32(n), true
}

// origins разбирает список origin через запятую. Не задано — пустой список
// (обязательность зависит от окружения — второй этап). Задано пустым —
// ошибка, как у остальных переменных.
//
// Формат строгий: браузер присылает Origin в каноническом виде (нижний
// регистр, без пути и порта по умолчанию), а сравнение — точное. Любое
// отклонение в конфиге означало бы origin, который никогда не совпадёт.
func (l *loader) origins(key string) (Origins, bool) {
	s, ok := l.lookup(key)
	if !ok {
		return Origins{}, true
	}
	if s == "" {
		l.fail(key, errEmpty)
		return Origins{}, false
	}
	var list []string
	for _, raw := range strings.Split(s, ",") {
		o := strings.TrimSpace(raw)
		if err := checkOrigin(o); err != nil {
			l.fail(key, err)
			return Origins{}, false
		}
		if slices.Contains(list, o) {
			l.fail(key, fmt.Errorf("duplicate origin %q", o))
			return Origins{}, false
		}
		list = append(list, o)
	}
	return Origins{joined: strings.Join(list, ",")}, true
}

func checkOrigin(o string) error {
	if o == "" {
		return errors.New("empty origin in the list")
	}
	if o == "*" || o == "null" {
		return fmt.Errorf("origin %q is not allowed: with credentials only exact origins are valid", o)
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(o, "/") {
		return fmt.Errorf("origin %q must be scheme://host[:port] without path, query or trailing slash", o)
	}
	if o != strings.ToLower(o) {
		return fmt.Errorf("origin %q must be lowercase: browsers send origins in lowercase", o)
	}
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		return fmt.Errorf("origin %q: drop the default port, browsers omit it", o)
	}
	return nil
}

func (l *loader) logLevel(key, def string) (slog.Level, bool) {
	s, ok := l.optional(key, def)
	if !ok {
		return 0, false
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		l.fail(key, err)
		return 0, false
	}
	return lvl, true
}
