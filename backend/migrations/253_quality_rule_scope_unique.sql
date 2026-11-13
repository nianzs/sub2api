-- Ported from ranxi2001/sub2api migration 258_quality_observation_scope.sql.
-- Observation records no account actions and can coexist with a quarantine
-- policy on the same account. Reserve one quality rule per account and scope,
-- including paused rules.
CREATE UNIQUE INDEX IF NOT EXISTS scheduled_test_quality_account_scope_unique
 ON scheduled_test_plans (account_id,
   (CASE pelican_config->'quality'->>'action'
      WHEN 'enable_bps' THEN 'bps'
      WHEN 'observe_only' THEN 'observation'
      ELSE 'quarantine' END))
 WHERE pelican_config->'quality' IS NOT NULL;
