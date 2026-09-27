-- Один INSERT … ON CONFLICT атомарен при любой конкуренции: блокировку
-- строки берёт Postgres, и два запроса не прочитают одно значение.

-- name: Hit :one
INSERT INTO platform.rate_limit_counters (key, window_start, hits, expires_at)
VALUES (@key, @window_start, 1, @expires_at)
ON CONFLICT (key, window_start) DO UPDATE SET hits = platform.rate_limit_counters.hits + 1
RETURNING hits;

-- name: DeleteExpired :execrows
DELETE FROM platform.rate_limit_counters
WHERE (key, window_start) IN (
    SELECT c.key, c.window_start FROM platform.rate_limit_counters c
    WHERE c.expires_at < @now
    LIMIT @batch_size
);
