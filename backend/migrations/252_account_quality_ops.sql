-- Ported from ranxi2001/sub2api migrations 247_account_quality_ops.sql and
-- 248_account_quality_judgment.sql. The per-account unique index from 247 is
-- superseded by the scope-based index in 253.
ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS quality_action TEXT NOT NULL DEFAULT '';
ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS quality_judgment JSONB;
ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS quality_round_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS account_quality_states (
 plan_id BIGINT PRIMARY KEY REFERENCES scheduled_test_plans(id) ON DELETE CASCADE,
 state JSONB NOT NULL DEFAULT '{}'
);
