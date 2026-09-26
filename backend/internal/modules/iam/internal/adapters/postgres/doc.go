// Package postgres — реализация портов на Postgres: sqlc-запросы модуля
// (queries/*.sql → sqlcdb/) и маппинг в доменные типы. Транзакцию берёт из
// контекста (platform/postgres.Tx); сам транзакций не открывает. Пакет
// platform/postgres импортируется под алиасом (pg), чтобы не путать имена.
package postgres
