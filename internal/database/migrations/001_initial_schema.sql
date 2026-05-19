-- 001_initial_schema.sql
-- Semantic Core RAG - Multi-user schema for SQLite

-- Plans
CREATE TABLE IF NOT EXISTS plans (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code VARCHAR(30) UNIQUE NOT NULL,
    name VARCHAR(100) NOT NULL,
    max_upload_mb INTEGER NOT NULL,
    daily_query_limit INTEGER NULL
);

INSERT OR IGNORE INTO plans (code, name, max_upload_mb, daily_query_limit) VALUES
    ('normal', 'Plan Normal', 50, 50),
    ('premium', 'Plan Premium', 150, NULL);

-- Users
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email VARCHAR(150) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    full_name VARCHAR(150) NOT NULL DEFAULT '',
    role VARCHAR(30) NOT NULL DEFAULT 'user',
    plan_id INTEGER REFERENCES plans(id) DEFAULT 1,
    active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Default admin user (password: admin123)
INSERT OR IGNORE INTO users (id, email, password_hash, full_name, role, plan_id, active) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin@rag.local',
     '$2a$10$k/SUxnAuVWKE4.vKteK9ROCmpgceBBtAv3TNqoTp4YuxWkQqp2uG6',
     'Administrador', 'admin', 2, 1);

-- Documents
CREATE TABLE IF NOT EXISTS documents (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'processing',
    chunks INTEGER NOT NULL DEFAULT 0,
    processed_chunks INTEGER NOT NULL DEFAULT 0,
    total_chunks INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Daily usage
CREATE TABLE IF NOT EXISTS daily_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    usage_date TEXT NOT NULL DEFAULT (DATE('now')),
    query_count INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, usage_date)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_documents_user_id ON documents(user_id);
CREATE INDEX IF NOT EXISTS idx_documents_status ON documents(status);
CREATE INDEX IF NOT EXISTS idx_daily_usage_user_date ON daily_usage(user_id, usage_date);
