-- 0001: ТЕХНИЧЕСКАЯ таблица для доказательства изоляции tenant'ов через RLS.
--
-- Не бизнес-данные и не часть модулей. Integration-тесты платформы пишут
-- сюда строки разных tenant'ов и проверяют, что RLS их разделяет. Образец
-- для tenant-таблиц модулей: ENABLE + FORCE RLS, политика по app.tenant_id,
-- гранты прикладной роли только на DML.

CREATE SCHEMA platform;
COMMENT ON SCHEMA platform IS
    'Technical objects of the platform layer. No business data.';

CREATE TABLE platform.rls_probe (
    id        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    note      text NOT NULL
);
COMMENT ON TABLE platform.rls_probe IS
    'TECHNICAL: RLS isolation probe for platform integration tests. Not business data.';

-- ENABLE включает RLS для всех, кроме владельца таблицы; FORCE — и для
-- владельца. Прикладная роль не владелец, так что для неё хватило бы
-- ENABLE; FORCE закрывает миграции и ручные сессии владельца.
ALTER TABLE platform.rls_probe ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe FORCE ROW LEVEL SECURITY;

-- current_setting(..., true) не падает, если параметр не задан. NULLIF нужен,
-- потому что после первой SET LOCAL на соединении параметр при следующих
-- транзакциях возвращается как '' (а не NULL), и ''::uuid — ошибка.
-- В обоих случаях сравнение с NULL ложно: без tenant — ноль строк.
--
-- Политика permissive. Дополнительные политики на tenant-таблицах должны
-- быть AS RESTRICTIVE: permissive-политики объединяются через OR и могут
-- ослабить изоляцию.
CREATE POLICY tenant_isolation ON platform.rls_probe
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

GRANT USAGE ON SCHEMA platform TO neurodent_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.rls_probe TO neurodent_app;
