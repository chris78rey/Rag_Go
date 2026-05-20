CREATE TABLE IF NOT EXISTS user_telegram_links (
    telegram_chat_id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_user_telegram_links_user_id ON user_telegram_links(user_id);
