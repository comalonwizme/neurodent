-- 0003: журнал аудита (ADR-0014). Класс audit: приложение только
-- добавляет события — без SELECT, UPDATE, DELETE, TRUNCATE.

CREATE SCHEMA audit;
COMMENT ON SCHEMA audit IS 'Audit log (ADR-0014). Append-only for the application role.';

-- Никакого PHI и свободного текста: только идентификаторы и коды. CHECK —
-- второй рубеж к проверкам в Go (platform/audit).
CREATE TABLE audit.events (
    id            uuid PRIMARY KEY DEFAULT uuidv7(), -- генерирует приложение; DEFAULT — страховка
    occurred_at   timestamptz NOT NULL,              -- shared/clock: когда произошло
    -- clock_timestamp(), а не now(): now() — время начала транзакции, для
    -- долгой транзакции оно раньше самого события.
    recorded_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    tenant_id     uuid,                              -- NULL — событие вне клиники
    actor_kind    text NOT NULL
        CHECK (actor_kind IN ('staff', 'patient_resolve', 'patient', 'system', 'user', 'anonymous')),
    actor_id      uuid,                              -- NULL для system и anonymous
    action        text NOT NULL
        CHECK (length(action) <= 100 AND action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2}$'),
    resource_type text NOT NULL
        CHECK (length(resource_type) <= 100 AND resource_type ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'),
    resource_id   uuid,
    outcome       text NOT NULL CHECK (outcome IN ('success', 'denied', 'failed')),
    request_id    text CHECK (request_id ~ '^[A-Za-z0-9._-]{8,64}$')
);
COMMENT ON TABLE audit.events IS
    'class=audit; Audit events (ADR-0014): ids and codes only, no PHI, no free text.';

CREATE INDEX events_tenant_occurred_at ON audit.events (tenant_id, occurred_at);

ALTER TABLE audit.events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.events FORCE ROW LEVEL SECURITY;

-- Событие согласовано со scope транзакции. IS NOT DISTINCT FROM — сравнение,
-- безопасное для NULL: у событий вне клиники tenant_id и текущий tenant оба
-- NULL, у system/anonymous — actor_id.
CREATE POLICY audit_insert ON audit.events FOR INSERT
    WITH CHECK (
        tenant_id IS NOT DISTINCT FROM (SELECT platform.current_tenant_id())
        AND actor_kind = (SELECT platform.current_actor_kind())
        AND actor_id IS NOT DISTINCT FROM (SELECT platform.current_actor_id()));

-- Чтение — для будущей роли чтения журнала (у прикладной роли SELECT нет):
-- только события своей клиники.
CREATE POLICY audit_read ON audit.events FOR SELECT
    USING (tenant_id = (SELECT platform.current_tenant_id()));

GRANT USAGE ON SCHEMA audit TO neurodent_app;
GRANT INSERT ON audit.events TO neurodent_app;
