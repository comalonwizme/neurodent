// Package http — хендлеры модуля: реализуют ServerInterface, сгенерированный
// по тегу модуля из api/openapi/openapi.yaml (ADR-0013). Тело читают через
// httpx.DecodeJSON, ответы и ошибки пишут через httpx. База данных отсюда
// не видна.
package http
