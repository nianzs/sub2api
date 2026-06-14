-- Kiro group-level cache-distribution reshaping center.
-- 0 = disabled (default, backfills existing rows). > 0 reshapes the emulated
-- cache split into an Anthropic-like distribution (tiny input, cache_read
-- dominant) for display realism only; it does not change billed totals.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS kiro_cache_force_ratio_center DECIMAL(5,4) NOT NULL DEFAULT 0;
