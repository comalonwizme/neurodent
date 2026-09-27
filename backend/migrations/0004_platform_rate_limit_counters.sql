-- 0004: счётчики точных лимитов (ADR-0016): вход, OTP — по аккаунту и
-- номеру. Класс cell-global. Ключ — HMAC-SHA256 с секретом приложения:
-- ни IP, ни телефонов, ни email в таблице нет, и перебором ключ не обратить.
CREATE TABLE platform.rate_limit_counters (
    key          bytea       NOT NULL CHECK (length(key) = 32),
    window_start timestamptz NOT NULL,
    hits         integer     NOT NULL,
    expires_at   timestamptz NOT NULL, -- конец окна: после него строку удаляет очистка
    PRIMARY KEY (key, window_start)
);
COMMENT ON TABLE platform.rate_limit_counters IS
    'class=cell-global; Rate limit counters (ADR-0016): HMAC keys only, no personal data.';

CREATE INDEX rate_limit_counters_expires_at ON platform.rate_limit_counters (expires_at);

-- RETURNING требует SELECT, очистке нужен DELETE.
GRANT SELECT, INSERT, UPDATE, DELETE ON platform.rate_limit_counters TO neurodent_app;
