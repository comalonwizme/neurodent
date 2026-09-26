# 0007. Драйвер Postgres: pgx v5

- Статус: Принято
- Дата: 2026-09-27

## Контекст

Основное хранилище — Postgres (см. [0004](0004-medical-record-ownership-and-rls.md)).
Нужны: пул соединений с лимитами и ротацией, транзакции с `SET LOCAL`,
типы Postgres (uuid, timestamptz, jsonb, массивы), серверные параметры
сессии (`statement_timeout`), контроль TLS.

## Решение

`github.com/jackc/pgx/v5` через нативный API (`pgxpool`, `pgx.Tx`), без
`database/sql`:

- `pgxpool` — пул с `MaxConns`, `MaxConnLifetime` и jitter;
- `RuntimeParams`: `statement_timeout`, `idle_in_transaction_session_timeout`,
  `application_name`;
- TLS обязателен вне dev: DSN, допускающий plaintext (включая
  `sslmode=prefer`), отклоняется при старте;
- ошибка разбора DSN возвращается без текста драйвера (DSN — секрет).

## Альтернативы

- **`database/sql` + pgx stdlib.** Переносимость между СУБД нам не нужна,
  а теряются типы Postgres, `CopyFrom`, пакетные запросы и явный контроль
  пула.
- **lib/pq.** В режиме поддержки, без новых возможностей.
- **ORM (gorm и т.п.).** Скрывает SQL, а с ним — и то, что уходит в
  медицинскую БД; RLS и `SET LOCAL` плохо ложатся на неявные транзакции ORM.
  Генерация типобезопасного кода из SQL (sqlc) остаётся открытым вопросом
  фазы 1 и совместима с pgx.

## Последствия

- Код модулей работает с `pgx.Tx` внутри `WithinTx`; тестирование — через
  integration-тесты на настоящем Postgres (testcontainers), а не моки.
- Зависимость: pgx и его транзитивные пакеты (puddle, x/text, x/sync);
  проверяются govulncheck в CI.
- Спроектировано по контролям: HIPAA 45 CFR §164.312(e)(1) (защита
  передачи); ISO/IEC 27001:2022 A.8.24 (использование криптографии).
