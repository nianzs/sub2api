-- Add grok-4.7 to the kiro platform's seeded channel-monitor model list.
--
-- Kiro started exposing xAI's Grok 4.7 on 2026-11-10. As with every other entry
-- in this list, it only affects the monitor's presentation dimension (listed
-- models get their own panel dimension, unlisted ones collapse into __other__).
-- It does not affect model availability or billing.
--
-- Like 197, this only touches rows still carrying the exact kiro model list that
-- 197 wrote. Any operator-customized list is left alone, and the EXISTS guard
-- also makes the statement idempotent: once grok-4.7 is present the list no
-- longer matches the 197 value, so re-running changes nothing.

UPDATE channel_monitor_v2_config AS t
SET platforms = (
        SELECT jsonb_agg(
                   CASE
                       WHEN elem->>'platform' = 'kiro'
                           AND elem->'models' = '["claude-opus-4-8","claude-opus-4-6","claude-opus-4-5","claude-sonnet-4-5","claude-sonnet-4-6","claude-haiku-4-5"]'::jsonb
                       THEN jsonb_set(elem, '{models}', (elem->'models') || '["grok-4.7"]'::jsonb)
                       ELSE elem
                   END
                   ORDER BY ord
               )
        FROM jsonb_array_elements(t.platforms) WITH ORDINALITY AS e(elem, ord)
    ),
    version = t.version + 1,
    updated_at = NOW()
WHERE t.id = 1
  AND EXISTS (
        SELECT 1
        FROM jsonb_array_elements(t.platforms) AS k
        WHERE k->>'platform' = 'kiro'
          AND k->'models' = '["claude-opus-4-8","claude-opus-4-6","claude-opus-4-5","claude-sonnet-4-5","claude-sonnet-4-6","claude-haiku-4-5"]'::jsonb
    );
