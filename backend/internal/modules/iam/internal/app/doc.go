// Package app — use cases модуля. Use case открывает транзакцию с явным
// scope (shared/scope) через порт; сетевого I/O внутри транзакции нет.
// Не видит pgx, net/http, platform/* и адаптеры.
package app
