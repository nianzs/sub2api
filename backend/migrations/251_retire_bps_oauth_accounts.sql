-- Excel BPS support was removed. bps-oauth accounts hold tokens issued to the
-- official Excel client: they cannot serve native Codex and can no longer be
-- refreshed, so retire them instead of letting the scheduler pick them.
-- Rows, credentials and extra are kept; admins delete the accounts themselves.
UPDATE accounts
SET status = 'error',
    schedulable = FALSE,
    error_message = 'Excel BPS support was removed; this bps-oauth account can no longer serve requests. Delete it or re-create it as a Codex OAuth account.',
    updated_at = NOW()
WHERE type = 'bps-oauth'
  AND deleted_at IS NULL;
