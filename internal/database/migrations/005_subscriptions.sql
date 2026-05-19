ALTER TABLE users ADD COLUMN subscription_expires_at TEXT;

UPDATE users
SET subscription_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+30 days')
WHERE subscription_expires_at IS NULL OR subscription_expires_at = '';

CREATE TABLE IF NOT EXISTS payments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount REAL NOT NULL,
    payment_date TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid_until TEXT NOT NULL,
    notes TEXT
);

CREATE INDEX IF NOT EXISTS idx_payments_user ON payments(user_id);
