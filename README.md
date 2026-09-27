# NeuroDent

Операционная система для стоматологических клиник: медзапись (EMR),
расписание, CRM, склад, финансы, кабинет пациента, AI-ассистент.
Работает с медицинскими данными (PHI), поэтому безопасность здесь —
требование, а не опция. Архитектура спроектирована по контролям HIPAA,
GDPR, ISO/IEC 27001/27701 и SOC 2 (см. ADR); это не заявление о
соответствии — его подтверждает только аудит.

Бэкенд — Go 1.27, модульный монолит ([ADR-0001](docs/adr/0001-modular-monolith.md)),
Postgres 18 с Row-Level Security.

## Быстрый старт (dev)

Нужны Go 1.27, Docker с Compose и `make`.

```bash
make db-env     # один раз: .env со случайными паролями БД и секретом лимитов (в .gitignore)
make db-up      # Postgres в Docker, порт только на 127.0.0.1
make migrate    # миграции от имени владельца схемы
make run        # API на http://127.0.0.1:8080
curl -i http://127.0.0.1:8080/readyz
```

`make db-down` останавливает БД, `make db-reset` удаляет её данные. Порт
Postgres меняется через `NEURODENT_PG_PORT`, если 5432 занят.

## Тесты и проверки

```bash
make test              # unit-тесты: -race -shuffle=on
make test-integration  # integration на настоящем Postgres (testcontainers, нужен Docker)
make cover             # покрытие по пакетам
make generate          # кодогенерация: OpenAPI → Go (oapi-codegen), SQL → Go (sqlc)
```

CI (`.github/workflows/ci.yml`) дополнительно проверяет gofmt, go vet,
golangci-lint, govulncheck, `go mod tidy` и то, что сгенерированный код
актуален. Правила зависимостей между модулями проверяет тест архитектуры
(`backend/tests/arch`).

## Устройство

```
api/openapi/          спецификация API — источник истины (ADR-0002, 0013)
backend/
  cmd/api, cmd/migrate
  internal/app/       composition root: сборка зависимостей, роутер, порядок middleware
  internal/modules/   бизнес-модули (iam, org — пока каркас); шаблон — ADR-0012
  internal/platform/  postgres, httpx, аудит, лимиты, логгер, health …
  internal/shared/    id, scope, clock, apperr, tenant — без инфраструктуры
  migrations/         SQL-миграции, встроены в бинарник
  tests/              integration и тест архитектуры
deploy/postgres/      роли БД (владелец схемы и прикладная роль)
docs/adr/             архитектурные решения
docs/notes/           разбор каждого этапа: что сделано, почему, мутации
```

Ключевое для нового кода:

- доступ к данным — только через `WithinTx` с явным scope (клиника + субъект),
  RLS не отдаёт чужие строки даже при ошибке в коде (ADR-0004, 0011, 0012);
- каждая таблица имеет класс (`tenant`, `patient-owned`, …) — тест схемы
  сверяет её политики с шаблоном класса (ADR-0011);
- ошибки API — RFC 9457 (ADR-0006), журнал аудита — без PHI (ADR-0014).

## Документы

- [Архитектурные решения (ADR)](docs/adr/README.md)
- [Заметки по этапам](docs/notes/)

Автор: Aliaidar Abilmansur
