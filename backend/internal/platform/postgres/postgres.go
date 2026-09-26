// Package postgres — пул соединений, транзакции с tenant-контекстом для RLS
// и мигратор.
//
// Весь доступ к данным идёт через DB.WithinTx: пул наружу не отдаётся,
// поэтому нельзя выполнить запрос мимо транзакции, в которой выставлен
// tenant. Запрос без tenant к tenant-таблице видит ноль строк (RLS).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ошибки конфигурации соединения. Текст ошибки драйвера не оборачивается:
// ошибка разбора DSN может процитировать его фрагменты, а DSN — секрет.
var (
	ErrInvalidDSN  = errors.New("invalid DSN (details withheld: the DSN may contain credentials)")
	ErrTLSRequired = errors.New("DSN allows a plaintext connection; use sslmode=require or stricter (verify-full recommended)")
)

// idleInTxTimeout — idle_in_transaction_session_timeout. Транзакция,
// брошенная посреди работы (баг, зависший хендлер), держит блокировки и
// соединение; Postgres оборвёт её сам. 30s больше любого разумного разрыва
// между запросами внутри транзакции и больше HandlerTimeout.
const idleInTxTimeout = 30 * time.Second

// Options — параметры пула. DSN — секрет: не логировать, не включать в ошибки.
type Options struct {
	DSN              string
	MaxConns         int32
	ConnectTimeout   time.Duration
	StatementTimeout time.Duration
	MaxConnLifetime  time.Duration
	// ApplicationName виден в pg_stat_activity: по нему DBA отличит API от
	// мигратора и ручных сессий.
	ApplicationName string
	// RequireTLS запрещает DSN, допускающий нешифрованное соединение
	// (включая sslmode=prefer с откатом на plaintext). Вне dev — всегда.
	RequireTLS bool
}

// DB — пул соединений прикладной роли.
type DB struct {
	pool *pgxpool.Pool
}

// New создаёт пул и проверяет соединение. Ошибка соединения на старте —
// отказ старта: неверный пароль или недоступная БД видны сразу при деплое.
func New(ctx context.Context, o Options) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(o.DSN)
	if err != nil {
		return nil, ErrInvalidDSN
	}
	if o.RequireTLS {
		if err := checkTLS(&cfg.ConnConfig.Config); err != nil {
			return nil, err
		}
	}

	cfg.MaxConns = o.MaxConns
	cfg.MaxConnLifetime = o.MaxConnLifetime
	// Разброс, чтобы соединения, открытые одновременно на старте, не
	// переоткрывались тоже одновременно.
	cfg.MaxConnLifetimeJitter = o.MaxConnLifetime / 10
	cfg.ConnConfig.ConnectTimeout = o.ConnectTimeout
	rp := cfg.ConnConfig.RuntimeParams
	rp["application_name"] = o.ApplicationName
	rp["statement_timeout"] = strconv.FormatInt(o.StatementTimeout.Milliseconds(), 10)
	rp["idle_in_transaction_session_timeout"] = strconv.FormatInt(idleInTxTimeout.Milliseconds(), 10)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, o.ConnectTimeout)
	defer cancel()
	// Ошибка соединения pgconn не содержит пароля (драйвер его вырезает),
	// а хост и пользователь нужны оператору для диагностики.
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// checkTLS требует TLS и для основного хоста, и для всех fallback'ов:
// sslmode=prefer даёт TLS первым, а plaintext — запасным вариантом.
func checkTLS(c *pgconn.Config) error {
	if c.TLSConfig == nil {
		return ErrTLSRequired
	}
	for _, fb := range c.Fallbacks {
		if fb.TLSConfig == nil {
			return ErrTLSRequired
		}
	}
	return nil
}

// Ping проверяет, что пул может выполнить запрос. Используется readiness.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	return nil
}

// Close закрывает пул, дожидаясь возврата занятых соединений. Реализует
// io.Closer для списка closers в app.
func (db *DB) Close() error {
	db.pool.Close()
	return nil
}

// Connect открывает одиночное соединение (для мигратора: advisory lock
// живёт в сессии, пул тут не нужен). Правила для DSN те же, что у New.
func Connect(ctx context.Context, dsn string, requireTLS bool, connectTimeout time.Duration, appName string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, ErrInvalidDSN
	}
	if requireTLS {
		if err := checkTLS(&cfg.Config); err != nil {
			return nil, err
		}
	}
	cfg.ConnectTimeout = connectTimeout
	cfg.RuntimeParams["application_name"] = appName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return conn, nil
}
