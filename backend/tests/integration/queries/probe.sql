-- Запросы к технической таблице platform.rls_probe для integration-тестов.

-- name: InsertProbe :exec
INSERT INTO platform.rls_probe (id, tenant_id, note) VALUES (@id, @tenant_id, @note);

-- name: GetProbe :one
SELECT id, tenant_id, note FROM platform.rls_probe WHERE id = @id;

-- name: ListProbeNotes :many
SELECT note FROM platform.rls_probe ORDER BY note;
