-- Клиника и субъект события не передаются из Go: их подставляет БД из
-- параметров транзакции (ADR-0014). Событие не может разойтись со scope,
-- под которым выполнено действие.

-- name: InsertEvent :exec
INSERT INTO audit.events (
    id, occurred_at, tenant_id, actor_kind, actor_id,
    action, resource_type, resource_id, outcome, request_id
) VALUES (
    @id, @occurred_at,
    platform.current_tenant_id(), platform.current_actor_kind(), platform.current_actor_id(),
    @action, @resource_type, sqlc.narg('resource_id'), @outcome, sqlc.narg('request_id')
);
