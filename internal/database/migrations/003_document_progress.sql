-- 003_document_progress.sql
-- Track per-document chunk progress for the web UI progress bar.

ALTER TABLE documents
  ADD COLUMN IF NOT EXISTS processed_chunks INTEGER NOT NULL DEFAULT 0;

ALTER TABLE documents
  ADD COLUMN IF NOT EXISTS total_chunks INTEGER NOT NULL DEFAULT 0;
