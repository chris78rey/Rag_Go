-- 002_system_settings.sql
-- Configuración global del sistema (título, nombre, etc.)

CREATE TABLE IF NOT EXISTS system_settings (
    key VARCHAR(100) PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO system_settings (key, value) VALUES
    ('app_title', 'Semantic Core RAG')
ON CONFLICT (key) DO NOTHING;
