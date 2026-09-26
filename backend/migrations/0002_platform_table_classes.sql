-- 0002: функции-аксессоры параметров транзакции и шаблоны политик классов
-- таблиц (ADR-0011). Все объекты — ТЕХНИЧЕСКИЕ (схема platform): probe-таблицы
-- доказывают шаблоны integration-тестами и служат образцом для модулей.
-- Бизнес-данных здесь нет.

-- Параметры транзакции выставляет WithinTx через SET LOCAL (ADR-0012).
-- NULLIF: на соединении, где уже был SET LOCAL, невыставленный параметр
-- равен '', а не NULL — ловушка закрыта здесь, в одном месте.
-- В политиках вызов оборачивается в (SELECT …): InitPlan вычисляет его один
-- раз на запрос, а не на каждую строку.
CREATE FUNCTION platform.current_tenant_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

CREATE FUNCTION platform.current_actor_kind() RETURNS text
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('app.actor_kind', true), '') $$;

CREATE FUNCTION platform.current_actor_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid $$;

CREATE FUNCTION platform.current_patient_id() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('app.patient_id', true), '')::uuid $$;

-- Принцип шаблонов: RESTRICTIVE-политики — инварианты изоляции (должны
-- выполняться все), PERMISSIVE — кому что разрешено. Имена фиксированы:
-- тест схемы сверяет точный набор политик каждого класса.

------------------------------------------------------------------------------
-- class=tenant: данные клиники, пациенту недоступные.
------------------------------------------------------------------------------
COMMENT ON TABLE platform.rls_probe IS
    'class=tenant; TECHNICAL: RLS probe of the tenant class for platform integration tests. Not business data.';

DROP POLICY tenant_isolation ON platform.rls_probe;

CREATE POLICY tenant_guard ON platform.rls_probe AS RESTRICTIVE
    USING (tenant_id = (SELECT platform.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT platform.current_tenant_id()));
CREATE POLICY staff_access ON platform.rls_probe
    USING ((SELECT platform.current_actor_kind()) IN ('staff', 'system'))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));

------------------------------------------------------------------------------
-- class=tenant-public: данные клиники, которые пациент читает.
------------------------------------------------------------------------------
CREATE TABLE platform.rls_probe_public (
    id        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    note      text NOT NULL
);
COMMENT ON TABLE platform.rls_probe_public IS
    'class=tenant-public; TECHNICAL: RLS probe of the tenant-public class. Not business data.';
ALTER TABLE platform.rls_probe_public ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe_public FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_guard ON platform.rls_probe_public AS RESTRICTIVE
    USING (tenant_id = (SELECT platform.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT platform.current_tenant_id()));
CREATE POLICY staff_access ON platform.rls_probe_public
    USING ((SELECT platform.current_actor_kind()) IN ('staff', 'system'))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));
CREATE POLICY patient_read ON platform.rls_probe_public FOR SELECT
    USING ((SELECT platform.current_actor_kind()) = 'patient');

------------------------------------------------------------------------------
-- class=patient-registry: карточки пациентов клиники и связи опекунства.
-- Карточка ссылается на аккаунт (user_id может быть пустым: пациент без
-- аккаунта). patient_resolve видит только свои карточки и карточки
-- подопечных (ADR-0011 п. 5).
------------------------------------------------------------------------------
CREATE TABLE platform.rls_probe_patients (
    id        uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id uuid NOT NULL,
    user_id   uuid,
    note      text NOT NULL
);
COMMENT ON TABLE platform.rls_probe_patients IS
    'class=patient-registry; TECHNICAL: RLS probe of patient cards. Not business data.';

CREATE TABLE platform.rls_probe_guardians (
    tenant_id        uuid NOT NULL,
    guardian_user_id uuid NOT NULL,
    ward_patient_id  uuid NOT NULL REFERENCES platform.rls_probe_patients (id),
    PRIMARY KEY (tenant_id, guardian_user_id, ward_patient_id)
);
COMMENT ON TABLE platform.rls_probe_guardians IS
    'class=patient-registry; TECHNICAL: RLS probe of guardianship links. Not business data.';

ALTER TABLE platform.rls_probe_patients ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe_patients FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe_guardians ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe_guardians FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_guard ON platform.rls_probe_guardians AS RESTRICTIVE
    USING (tenant_id = (SELECT platform.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT platform.current_tenant_id()));
CREATE POLICY subject_guard ON platform.rls_probe_guardians AS RESTRICTIVE
    USING (
        (SELECT platform.current_actor_kind()) IN ('staff', 'system')
        OR ((SELECT platform.current_actor_kind()) = 'patient_resolve'
            AND guardian_user_id = (SELECT platform.current_actor_id())))
    WITH CHECK (
        (SELECT platform.current_actor_kind()) IN ('staff', 'system')
        OR ((SELECT platform.current_actor_kind()) = 'patient_resolve'
            AND guardian_user_id = (SELECT platform.current_actor_id())));
CREATE POLICY staff_access ON platform.rls_probe_guardians
    USING ((SELECT platform.current_actor_kind()) IN ('staff', 'system'))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));
CREATE POLICY patient_read ON platform.rls_probe_guardians FOR SELECT
    USING ((SELECT platform.current_actor_kind()) IN ('patient', 'patient_resolve'));

CREATE POLICY tenant_guard ON platform.rls_probe_patients AS RESTRICTIVE
    USING (tenant_id = (SELECT platform.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT platform.current_tenant_id()));
-- Подзапрос к связям опекунства идёт под их собственными политиками:
-- patient_resolve видит там только свои связи.
CREATE POLICY subject_guard ON platform.rls_probe_patients AS RESTRICTIVE
    USING (
        (SELECT platform.current_actor_kind()) IN ('staff', 'system')
        OR ((SELECT platform.current_actor_kind()) = 'patient'
            AND id = (SELECT platform.current_patient_id()))
        OR ((SELECT platform.current_actor_kind()) = 'patient_resolve'
            AND (user_id = (SELECT platform.current_actor_id())
                 OR id IN (SELECT g.ward_patient_id FROM platform.rls_probe_guardians g
                           WHERE g.guardian_user_id = (SELECT platform.current_actor_id())))))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));
CREATE POLICY staff_access ON platform.rls_probe_patients
    USING ((SELECT platform.current_actor_kind()) IN ('staff', 'system'))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));
CREATE POLICY patient_read ON platform.rls_probe_patients FOR SELECT
    USING ((SELECT platform.current_actor_kind()) IN ('patient', 'patient_resolve'));

------------------------------------------------------------------------------
-- class=patient-owned: данные о конкретном пациенте клиники.
------------------------------------------------------------------------------
CREATE TABLE platform.rls_probe_records (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    tenant_id  uuid NOT NULL,
    patient_id uuid NOT NULL REFERENCES platform.rls_probe_patients (id),
    note       text NOT NULL
);
COMMENT ON TABLE platform.rls_probe_records IS
    'class=patient-owned; TECHNICAL: RLS probe of the patient-owned class. Not business data.';
ALTER TABLE platform.rls_probe_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.rls_probe_records FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_guard ON platform.rls_probe_records AS RESTRICTIVE
    USING (tenant_id = (SELECT platform.current_tenant_id()))
    WITH CHECK (tenant_id = (SELECT platform.current_tenant_id()));
CREATE POLICY subject_guard ON platform.rls_probe_records AS RESTRICTIVE
    USING (
        (SELECT platform.current_actor_kind()) IN ('staff', 'system')
        OR ((SELECT platform.current_actor_kind()) = 'patient'
            AND patient_id = (SELECT platform.current_patient_id())))
    WITH CHECK (
        (SELECT platform.current_actor_kind()) IN ('staff', 'system')
        OR ((SELECT platform.current_actor_kind()) = 'patient'
            AND patient_id = (SELECT platform.current_patient_id())));
CREATE POLICY staff_access ON platform.rls_probe_records
    USING ((SELECT platform.current_actor_kind()) IN ('staff', 'system'))
    WITH CHECK ((SELECT platform.current_actor_kind()) IN ('staff', 'system'));
CREATE POLICY patient_access ON platform.rls_probe_records
    USING ((SELECT platform.current_actor_kind()) = 'patient')
    WITH CHECK ((SELECT platform.current_actor_kind()) = 'patient');

GRANT SELECT, INSERT, UPDATE, DELETE
    ON platform.rls_probe_public, platform.rls_probe_patients,
       platform.rls_probe_guardians, platform.rls_probe_records
    TO neurodent_app;
