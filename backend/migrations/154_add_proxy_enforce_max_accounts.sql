-- Add per-proxy toggle deciding whether max_accounts is a hard limit.
-- Default FALSE = soft advisory limit (overflow logged, binding allowed), so
-- upgrading never blocks existing proxy bindings; operators opt in to hard
-- enforcement per proxy.
ALTER TABLE proxies
    ADD COLUMN IF NOT EXISTS enforce_max_accounts BOOLEAN NOT NULL DEFAULT FALSE;
