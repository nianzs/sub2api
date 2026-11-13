-- Ported from ranxi2001/sub2api migration 326_quality_shared_batches.sql.
-- Runtime-only shared random probes. No credentials, client prompts or fixed
-- public question bank; old binaries ignore these additive tables.
CREATE TABLE IF NOT EXISTS quality_probe_batches (
    id TEXT PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    start_deadline TIMESTAMPTZ NOT NULL,
    closes_at TIMESTAMPTZ NOT NULL,
    questions JSONB NOT NULL,
    CHECK (started_at < start_deadline AND start_deadline < closes_at)
);
CREATE INDEX IF NOT EXISTS quality_probe_batches_retention ON quality_probe_batches (closes_at);

CREATE TABLE IF NOT EXISTS quality_probe_batch_runs (
    batch_id TEXT NOT NULL REFERENCES quality_probe_batches(id) ON DELETE CASCADE,
    plan_id BIGINT NOT NULL REFERENCES scheduled_test_plans(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    snapshot JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','collected','finalized')),
    PRIMARY KEY (batch_id, plan_id)
);
CREATE INDEX IF NOT EXISTS quality_probe_batch_runs_pending ON quality_probe_batch_runs (batch_id, status);
