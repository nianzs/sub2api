-- Excel BPS（bps-oauth 账号类型与 basispoints 协议）已下线，清理残留数据。
--
-- 1. 软删除 bps-oauth 账号，及以它们为母账号的 spark 影子账号，效果与后台逐个
--    删除相同：解除分组绑定、删除定时测试计划、写 deleted_at，并发 account_changed
--    事件，让运行中的实例把这些账号移出调度快照。代码删除后这些账号已无法转发；
--    若保留，调度仍可能选中它们，导致请求失败。
-- 2. 清理账号 extra 中的 BPS 设置 openai_excel_bps*（openai_excel_bps 前缀的所有键）。
-- 3. 删除早已废弃的全局开关 excel_bps_enabled。
--
-- 只改命中的行，不碰其他账号的 updated_at；可重复执行。

CREATE TEMP TABLE bps_oauth_removed_accounts ON COMMIT DROP AS
SELECT id
FROM accounts
WHERE deleted_at IS NULL
  AND (
        type = 'bps-oauth'
        OR parent_account_id IN (
            SELECT id FROM accounts WHERE type = 'bps-oauth' AND deleted_at IS NULL
        )
    );

DELETE FROM account_groups
WHERE account_id IN (SELECT id FROM bps_oauth_removed_accounts);

DELETE FROM scheduled_test_plans
WHERE account_id IN (SELECT id FROM bps_oauth_removed_accounts);

WITH removed AS (
    UPDATE accounts
    SET deleted_at = NOW(),
        schedulable = FALSE,
        updated_at = NOW()
    WHERE id IN (SELECT id FROM bps_oauth_removed_accounts)
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id
FROM removed;

UPDATE accounts
SET extra = extra - ARRAY(
        SELECT k
        FROM jsonb_object_keys(extra) AS k
        WHERE k LIKE 'openai\_excel\_bps%'
    ),
    updated_at = NOW()
WHERE jsonb_typeof(extra) = 'object'
  AND EXISTS (
        SELECT 1
        FROM jsonb_object_keys(extra) AS k
        WHERE k LIKE 'openai\_excel\_bps%'
    );

DELETE FROM settings
WHERE key = 'excel_bps_enabled';
