-- Add per-proxy account-binding quota (Kiro forced-proxy quota).
-- Default 3 also backfills existing rows; NOT NULL keeps HasCapacity well-defined.
ALTER TABLE proxies
    ADD COLUMN IF NOT EXISTS max_accounts INTEGER NOT NULL DEFAULT 3;
