-- 004_plan_limits_and_rate_limits.sql
-- Final quota model: repo slots, storage caps and per-minute rate limits.

UPDATE plans
SET max_upload_mb = 50,
    daily_query_limit = 50
WHERE code = 'normal';

UPDATE plans
SET max_upload_mb = 150,
    daily_query_limit = NULL
WHERE code = 'premium';

CREATE TABLE IF NOT EXISTS plan_limits (
    plan_code TEXT PRIMARY KEY REFERENCES plans(code) ON DELETE CASCADE,
    max_repositories INTEGER NOT NULL,
    max_total_storage_mb INTEGER NOT NULL,
    queries_per_minute INTEGER NOT NULL,
    uploads_per_minute INTEGER NOT NULL,
    max_concurrent_uploads INTEGER NOT NULL
);

INSERT OR REPLACE INTO plan_limits (
    plan_code,
    max_repositories,
    max_total_storage_mb,
    queries_per_minute,
    uploads_per_minute,
    max_concurrent_uploads
) VALUES
    ('normal', 3, 150, 6, 2, 1),
    ('premium', 10, 1500, 20, 4, 3);

CREATE TABLE IF NOT EXISTS request_rate_limits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    window_key TEXT NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, action, window_key)
);

CREATE INDEX IF NOT EXISTS idx_request_rate_limits_user_action_window
    ON request_rate_limits(user_id, action, window_key);
