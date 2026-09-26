// Package config загружает и валидирует конфигурацию процесса из переменных
// окружения. Конфиг читается один раз на старте; любая ошибка — отказ старта.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"
)

// Env — окружение, в котором запущен процесс. Значение типа Env можно получить
// только через parseEnv, поэтому после Load() невалидного Env не существует.
type Env string

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
	keyIdleTimeout       = "NEURODENT_IDLE_TIMEOUT"
	keyShutdownTimeout   = "NEURODENT_SHUTDOWN_TIMEOUT"
	keyDrainDelay        = "NEURODENT_DRAIN_DELAY"
	keyLogLevel          = "NEURODENT_LOG_LEVEL"
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

	// Хендлер + запись ответа. Timeout-middleware (шаг 0.3) будет ~10s — меньше
	// этого значения, чтобы клиент получил нормальный 503, а не оборванное соединение.
	defaultWriteTimeout = 15 * time.Second

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
)

// bounds — допустимый диапазон для duration-переменной. Верхняя граница ловит
// опечатки вида WRITE_TIMEOUT=10h, которые молча выключают защиту.
type bounds struct{ min, max time.Duration }

// errEmpty — sentinel-ошибка. Единственное допустимое исключение из правила
// «без глобальных переменных»: её никто не изменяет (как io.EOF в stdlib).
var errEmpty = errors.New("set but empty")

// Config — значение, а не указатель: после Load() он иммутабелен
// и передаётся копией.
type Config struct {
	Env               Env
	HTTPAddr          string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	DrainDelay        time.Duration
	LogLevel          slog.Level
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
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Duration("drain_delay", c.DrainDelay),
		slog.String("log_level", c.LogLevel.String()),
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
	it, _ := l.duration(keyIdleTimeout, defaultIdleTimeout, bounds{10 * time.Second, 5 * time.Minute})
	st, stOK := l.duration(keyShutdownTimeout, defaultShutdownTimeout, bounds{time.Second, stopBudget})

	drainDefault := defaultDrainDelay
	if envOK && env == EnvDev {
		drainDefault = defaultDrainDelayDev
	}
	dd, ddOK := l.duration(keyDrainDelay, drainDefault, bounds{0, 10 * time.Second})

	lvl, lvlOK := l.logLevel(keyLogLevel, defaultLogLevel)

	// Этап 2: кросс-полевые инварианты — только если все участники валидны.
	// Иначе одна опечатка порождает каскад ложных ошибок про соседние поля.
	if rhtOK && rtOK && rht > rt {
		l.fail(keyReadHeaderTimeout, fmt.Errorf("must be <= %s", keyReadTimeout))
	}
	if stOK && wtOK && st < wt {
		l.fail(keyShutdownTimeout, fmt.Errorf("must be >= %s, otherwise in-flight requests are cut", keyWriteTimeout))
	}
	if stOK && ddOK && st+dd > stopBudget {
		l.fail(keyShutdownTimeout, fmt.Errorf("%s + %s must be <= %s (orchestrator grace period minus close reserve)", keyShutdownTimeout, keyDrainDelay, stopBudget))
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
		IdleTimeout:       it,
		ShutdownTimeout:   st,
		DrainDelay:        dd,
		LogLevel:          lvl,
	}, nil
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
