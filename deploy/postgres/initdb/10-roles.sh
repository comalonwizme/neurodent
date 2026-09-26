#!/usr/bin/env bash
# Роли и база NeuroDent. Выполняется entrypoint'ом официального образа
# postgres один раз — при инициализации пустого data-каталога — от
# суперпользователя. Тот же скрипт используют integration-тесты
# (testcontainers), поэтому модель ролей в dev и тестах одна.
#
# Пароли приходят из окружения контейнера (docker-compose берёт их из
# локального .env, который создаёт `make db-env`; в репозиторий не попадают).
set -euo pipefail

: "${NEURODENT_DB_NAME:=neurodent}"
: "${NEURODENT_PG_OWNER_PASSWORD:?must be set}"
: "${NEURODENT_PG_APP_PASSWORD:?must be set}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -v db="$NEURODENT_DB_NAME" \
    -v owner_pw="$NEURODENT_PG_OWNER_PASSWORD" \
    -v app_pw="$NEURODENT_PG_APP_PASSWORD" <<'SQL'
-- Владелец схемы: под ним работает только cmd/migrate (DDL).
CREATE ROLE neurodent_owner LOGIN PASSWORD :'owner_pw'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

-- Прикладная роль API: только DML по явным грантам из миграций. Не владеет
-- таблицами, поэтому не может отключить RLS или изменить политики; без
-- BYPASSRLS; NOINHERIT — не наследует права ролей, в которые её включат.
CREATE ROLE neurodent_app LOGIN PASSWORD :'app_pw'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;

CREATE DATABASE :"db" OWNER neurodent_owner;
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO neurodent_app;

\connect :"db"
-- С PG15 CREATE на public отозван у PUBLIC; убираем и USAGE: прикладной
-- роли в public делать нечего (там лежит schema_migrations).
REVOKE ALL ON SCHEMA public FROM PUBLIC;
SQL
