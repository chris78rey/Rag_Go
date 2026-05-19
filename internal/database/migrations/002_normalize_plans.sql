-- 002_normalize_plans.sql
-- Rename legacy plan codes to normal/premium while preserving plan IDs.

UPDATE plans
SET code = 'normal',
    name = 'Plan Normal',
    max_upload_mb = 50,
    daily_query_limit = 50
WHERE code = 'basic';

UPDATE plans
SET code = 'premium',
    name = 'Plan Premium',
    max_upload_mb = 150,
    daily_query_limit = NULL
WHERE code = 'unlimited';
