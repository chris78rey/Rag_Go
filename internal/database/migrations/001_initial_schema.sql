-- 001_initial_schema.sql
-- Semantic Core RAG - Multi-user schema

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Plans
CREATE TABLE IF NOT EXISTS plans (
    id SERIAL PRIMARY KEY,
    code VARCHAR(30) UNIQUE NOT NULL,
    name VARCHAR(100) NOT NULL,
    max_upload_mb INTEGER NOT NULL,
    daily_query_limit INTEGER NULL
);

INSERT INTO plans (code, name, max_upload_mb, daily_query_limit) VALUES
    ('basic', 'Plan Básico', 100, 40),
    ('unlimited', 'Plan Ilimitado', 1024, NULL)
ON CONFLICT (code) DO NOTHING;

-- Users
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email VARCHAR(150) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    full_name VARCHAR(150) NOT NULL DEFAULT '',
    role VARCHAR(30) NOT NULL DEFAULT 'user',
    plan_id INTEGER REFERENCES plans(id) DEFAULT 1,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Default admin user (password: admin123)
INSERT INTO users (id, email, password_hash, full_name, role, plan_id, active) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin@rag.local',
     '$2a$10$k/SUxnAuVWKE4.vKteK9ROCmpgceBBtAv3TNqoTp4YuxWkQqp2uG6',
     'Administrador', 'admin', 2, true)
ON CONFLICT (email) DO NOTHING;

-- Documents
CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'processing',
    chunks INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Daily usage
CREATE TABLE IF NOT EXISTS daily_usage (
    id SERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    usage_date DATE NOT NULL DEFAULT CURRENT_DATE,
    query_count INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, usage_date)
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_documents_user_id ON documents(user_id);
CREATE INDEX IF NOT EXISTS idx_documents_status ON documents(status);
CREATE INDEX IF NOT EXISTS idx_daily_usage_user_date ON daily_usage(user_id, usage_date);
