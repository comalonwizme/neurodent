.PHONY: test cover generate db-env db-up db-down db-reset migrate run test-integration

# Локальные пароли БД (создаёт db-env, файл в .gitignore).
-include .env

NEURODENT_PG_PORT ?= 5432
# DSN локальной БД: $(call pg_dsn,роль,пароль). Только dev: sslmode=disable.
pg_dsn = postgres://$(1):$(2)@127.0.0.1:$(NEURODENT_PG_PORT)/neurodent?sslmode=disable

test:
	go -C backend test -race -shuffle=on -count=1 -timeout=60s ./...

cover:
	go -C backend test -count=1 -coverprofile=../cover.out ./... && go -C backend tool cover -func=../cover.out

# Кодогенерация (ADR-0013): сервер из api/openapi/openapi.yaml (по тегу —
# свой конфиг рядом с пакетом), запросы sqlc. Инструменты — из backend/tools/go.mod,
# версии закреплены там; основной go.mod они не трогают. sqlc собирается без cgo.
# Сгенерированный код коммитится; CI проверяет, что он актуален.
generate:
	cd backend && for cfg in $$(find internal -name oapi-codegen.yaml | sort); do \
	  go tool -modfile=tools/go.mod oapi-codegen -config $$cfg ../api/openapi/openapi.yaml || exit 1; \
	done
	cd backend && CGO_ENABLED=0 go tool -modfile=tools/go.mod sqlc generate

# Генерирует случайные пароли один раз. Не перезаписывает существующий .env:
# пароли уже зашиты в том данных. Для смены — make db-reset и удалить .env.
db-env:
	@if [ -f .env ]; then echo ".env exists, keeping it"; else \
	  umask 077 && { \
	    echo "NEURODENT_PG_SUPERUSER_PASSWORD=$$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"; \
	    echo "NEURODENT_PG_OWNER_PASSWORD=$$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"; \
	    echo "NEURODENT_PG_APP_PASSWORD=$$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"; \
	  } > .env && echo "created .env with local database passwords"; \
	fi
	@# Секрет HMAC ключей rate limit (ADR-0016): 32 байта. Дописывается и в
	@# .env, созданный до его появления.
	@grep -q '^NEURODENT_RATELIMIT_KEY=' .env || { \
	  echo "NEURODENT_RATELIMIT_KEY=$$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" >> .env && \
	  echo "added NEURODENT_RATELIMIT_KEY to .env"; }

db-up: db-env
	docker compose up -d --wait postgres

db-down:
	docker compose down

# Удаляет том с данными: init-скрипт ролей выполнится заново.
db-reset:
	docker compose down -v

# @ — чтобы команда с паролем не печаталась в терминал.
migrate:
	@NEURODENT_ENV=dev NEURODENT_MIGRATE_DSN='$(call pg_dsn,neurodent_owner,$(NEURODENT_PG_OWNER_PASSWORD))' \
	  go -C backend run ./cmd/migrate

run:
	@NEURODENT_ENV=dev NEURODENT_DB_DSN='$(call pg_dsn,neurodent_app,$(NEURODENT_PG_APP_PASSWORD))' \
	  NEURODENT_RATELIMIT_KEY='$(NEURODENT_RATELIMIT_KEY)' \
	  go -C backend run ./cmd/api

test-integration:
	go -C backend test -race -count=1 -timeout=10m -tags=integration ./tests/integration/...
